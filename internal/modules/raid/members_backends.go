package raid

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"toolctl/internal/core"
)

func (r *Runner) memberQuery(ctx context.Context, binary string, args []string) ([]byte, error) {
	if binary == "" {
		return nil, fmt.Errorf("management tool is unavailable")
	}
	if !architectureCompatible(binary, r.deps.Architecture) {
		return nil, fmt.Errorf("management tool architecture mismatch")
	}
	res := r.deps.Processes.Run(ctx, core.ProcessSpec{Path: binary, Args: args, Env: []string{"LC_ALL=C", "LANG=C"}, MaxStdout: 16 << 20, MaxStderr: 1 << 20})
	if res.Err != nil || res.ExitCode != 0 || res.TimedOut || res.StdoutExceeded || res.StderrExceeded {
		return nil, fmt.Errorf("query %q failed: %s", strings.Join(args, " "), processMessage(res))
	}
	lower := strings.ToLower(string(res.Stdout))
	for _, line := range strings.Split(lower, "\n") {
		line = strings.TrimSpace(line)
		if line == "command failed" || line == "command failed." {
			return nil, fmt.Errorf("query %q: %s", strings.Join(args, " "), processMessage(res))
		}
	}
	for _, match := range megaExitCode.FindAllStringSubmatch(lower, -1) {
		code, err := strconv.ParseUint(match[1], 16, 64)
		if err != nil || code != 0 {
			return nil, fmt.Errorf("query %q reported MegaCLI exit code 0x%s", strings.Join(args, " "), match[1])
		}
	}
	return res.Stdout, nil
}

func (r *Runner) arcconfMembers(ctx context.Context, binary string) ([]Member, error) {
	listed, err := r.memberQuery(ctx, binary, []string{"LIST", "nologs"})
	ids := arcconfControllerIDs(string(listed))
	if err != nil || len(ids) == 0 {
		listed, err = r.memberQuery(ctx, binary, []string{"GETVERSION", "nologs"})
		if err != nil {
			return nil, err
		}
		ids = arcconfControllerIDs(string(listed))
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("arcconf did not report controller IDs")
	}
	rows := []Member{}
	failures := []error{}
	for _, id := range ids {
		data, e := r.memberQuery(ctx, binary, []string{"GETCONFIG", id, "AL", "nologs"})
		if e != nil {
			failures = append(failures, e)
			continue
		}
		part, e := parseArcconfMembers(data, id)
		if e != nil {
			failures = append(failures, fmt.Errorf("arcconf controller %s: %w", id, e))
			continue
		}
		rows = append(rows, part...)
	}
	return rows, errors.Join(failures...)
}

var (
	arcSegment      = regexp.MustCompile(`(?i)^(?:Group\s+([0-9]+),\s*)?Segment\s+([0-9]+)\s*:\s*(.*)$`)
	arcNamedAddress = regexp.MustCompile(`(?i)(?:Channel|Connector)\s*:\s*([0-9]+)\s*,\s*Device\s*:\s*([0-9]+)`)
	arcPairAddress  = regexp.MustCompile(`\(\s*([0-9]+)\s*,\s*([0-9]+)\s*\)`)
	megaDiskGroup   = regexp.MustCompile(`(?i)^DISK\s+GROUP\s*:\s*([0-9]+)`)
	ssaArray        = regexp.MustCompile(`(?i)^array\s+(\S+)`)
	megaExitCode    = regexp.MustCompile(`(?im)^[ \t]*exit\s+code\s*:\s*0x([0-9a-f]+)`)
)

// Arcconf channel/device and connector/device are transport addresses. Only
// Reported Location containing Enclosure/Slot supplies a physical bay number.
func parseArcconfMembers(data []byte, controller string) ([]Member, error) {
	inv, err := parseArcconf(data, controller)
	if err != nil {
		return nil, err
	}
	volumes := map[string]Volume{}
	for _, v := range inv.Volumes {
		volumes[v.ID] = v
	}
	rows := []Member{}
	volume := ""
	counts := map[string]int{}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if match := arcLogicalID.FindStringSubmatch(line); len(match) == 2 {
			volume = match[1]
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "physical device information") {
			volume = ""
		}
		seg := arcSegment.FindStringSubmatch(line)
		if len(seg) == 0 || volume == "" {
			continue
		}
		counts[volume]++
		value := seg[3]
		address := ""
		if m := arcNamedAddress.FindStringSubmatch(value); len(m) == 3 {
			address = m[1] + ":" + m[2]
		}
		if address == "" {
			if m := arcPairAddress.FindStringSubmatch(value); len(m) == 3 {
				address = m[1] + ":" + m[2]
			}
		}
		serial := ""
		if i := strings.LastIndex(value, ")"); i >= 0 {
			serial = strings.TrimSpace(value[i+1:])
		}
		candidates := []Disk{}
		// A serial is independent of the address spelling used by arcconf.
		if serial != "" {
			for _, d := range inv.Disks {
				if d.Serial == serial {
					candidates = append(candidates, d)
				}
			}
		}
		if len(candidates) == 0 && address != "" {
			for _, d := range inv.Disks {
				if d.ID == address {
					candidates = append(candidates, d)
				}
			}
		}
		missing := strings.HasPrefix(strings.ToLower(value), "missing") || strings.HasPrefix(strings.ToLower(value), "not present")
		var disk Disk
		switch {
		case len(candidates) == 1:
			disk = candidates[0]
		case len(candidates) > 1:
			return nil, fmt.Errorf("volume %s segment %s has ambiguous physical-drive matches", volume, seg[2])
		case missing:
			disk = Disk{Controller: controller, ID: firstNonEmpty(address, "missing-"+seg[1]+"-"+seg[2]), Serial: serial, State: HealthFailed, VendorState: "Missing", Backend: "arcconf"}
		default:
			return nil, fmt.Errorf("volume %s segment %s could not be matched to physical inventory (address=%s serial=%s)", volume, seg[2], address, serial)
		}
		if missing {
			disk.VendorState = "Missing"
			disk.State = HealthFailed
		}
		rows = append(rows, Member{Controller: controller, Volume: volume, RAIDLevel: volumes[volume].RAIDLevel, Span: seg[1], Address: address, Disk: disk})
	}
	for _, v := range inv.Volumes {
		if counts[v.ID] == 0 {
			return nil, fmt.Errorf("volume %s has no reported segment members", v.ID)
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("arcconf did not report logical-device segments")
	}
	return rows, nil
}

// CfgDsply explicitly groups virtual drives and physical drives under DISK
// GROUP. A disk group can back multiple VDs, all sharing its physical members.
func parseMegaCLIConfigMembers(data []byte) ([]Member, error) {
	rows := []Member{}
	controller, group := "", ""
	lines := []string{}
	flush := func() error {
		if group == "" {
			return nil
		}
		inv := newVendorInventory()
		block := []byte("Adapter #" + controller + "\n" + strings.Join(lines, "\n"))
		if e := parseMegaCLIVolumes(block, &inv); e != nil {
			return e
		}
		if e := parseMegaCLIDisks(block, &inv); e != nil {
			return e
		}
		for _, v := range inv.Volumes {
			// Parse the VD-only section for RAID10/50/60 metadata, then attach
			// the group's explicit physical inventory.
			raid := v.RAIDLevel
			active := false
			for _, line := range lines {
				if m := megaVirtualDrive.FindStringSubmatch(line); len(m) == 2 {
					active = m[1] == v.ID
				}
				key, value, ok := splitVendorField(line)
				depth, _ := strconv.Atoi(strings.TrimSpace(value))
				if active && ok && strings.EqualFold(key, "Span Depth") && depth > 1 {
					switch raid {
					case "RAID 1":
						raid = "RAID 10"
					case "RAID 5":
						raid = "RAID 50"
					case "RAID 6":
						raid = "RAID 60"
					}
				}
			}
			for _, d := range inv.Disks {
				rows = append(rows, Member{Controller: controller, Volume: v.ID, DiskGroup: group, RAIDLevel: raid, Disk: d})
			}
		}
		return nil
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if m := megaAdapterHeader.FindStringSubmatch(line); len(m) == 2 {
			if e := flush(); e != nil {
				return nil, e
			}
			controller = m[1]
			group = ""
			lines = nil
			continue
		}
		if m := megaDiskGroup.FindStringSubmatch(line); len(m) == 2 {
			if e := flush(); e != nil {
				return nil, e
			}
			if controller == "" {
				return nil, fmt.Errorf("disk group without adapter ID")
			}
			group = m[1]
			lines = nil
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "global hot spare") || strings.HasPrefix(lower, "hot spares") || strings.HasPrefix(lower, "number of global hot spare") || strings.HasPrefix(lower, "unconfigured physical") {
			if e := flush(); e != nil {
				return nil, e
			}
			group = ""
			lines = nil
			continue
		}
		if group != "" {
			lines = append(lines, line)
		}
	}
	if e := flush(); e != nil {
		return nil, e
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("CfgDsply did not report disk-group member mapping")
	}
	return rows, nil
}

func parseSSACLIMembers(data []byte) ([]Member, error) {
	rows := []Member{}
	header, array := "", ""
	lines := []string{}
	flush := func() error {
		if array == "" {
			return nil
		}
		inv, e := parseSSACLI([]byte(header + "\n" + strings.Join(lines, "\n")))
		if e != nil {
			return e
		}
		if len(inv.Volumes) > 0 && len(inv.Disks) == 0 {
			return fmt.Errorf("SSA array %s has no reported physical members", array)
		}
		for _, v := range inv.Volumes {
			for _, d := range inv.Disks {
				if strings.Contains(strings.ToLower(d.MediaType), "spare") {
					continue
				}
				rows = append(rows, Member{Controller: v.Controller, Volume: v.ID, DiskGroup: array, RAIDLevel: normalizeSSARaid(v.RAIDLevel), Disk: d})
			}
		}
		return nil
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if m := ssaControllerHeader.FindStringSubmatch(line); len(m) == 3 {
			if e := flush(); e != nil {
				return nil, e
			}
			header = line
			array = ""
			lines = nil
			continue
		}
		if m := ssaArray.FindStringSubmatch(line); len(m) == 2 {
			if e := flush(); e != nil {
				return nil, e
			}
			array = m[1]
			lines = nil
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "unassigned") {
			if e := flush(); e != nil {
				return nil, e
			}
			array = ""
			lines = nil
			continue
		}
		if array != "" {
			lines = append(lines, line)
		}
	}
	if e := flush(); e != nil {
		return nil, e
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("SSA CLI did not report array member mapping")
	}
	return rows, nil
}
