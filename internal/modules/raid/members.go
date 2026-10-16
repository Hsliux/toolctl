package raid

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

type Member struct {
	Controller string `json:"controller"`
	Volume     string `json:"volume"`
	RAIDLevel  string `json:"raidLevel"`
	Span       string `json:"span,omitempty"`
	Disk
}

func memberColumns() []v1alpha1.ColumnHint {
	fields := []struct {
		header, path string
		wide         bool
	}{
		{"CONTROLLER", "controller", false}, {"VOLUME", "volume", false}, {"RAID", "raidLevel", false}, {"SPAN", "span", false},
		{"ENCLOSURE", "enclosure", false}, {"SLOT", "slot", false}, {"DEVICE ID", "id", false}, {"STATUS", "status", false},
		{"MODEL", "model", true}, {"SERIAL", "serial", true}, {"BACKEND", "backend", true}, {"MEGACLI ARGS", "megacliArgs", true},
	}
	columns := []v1alpha1.ColumnHint{}
	for i, f := range fields {
		columns = append(columns, v1alpha1.ColumnHint{Header: f.header, Path: "data." + f.path, Type: v1alpha1.ColumnString, Wide: f.wide, Order: i})
	}
	return columns
}

func (r *Runner) members(ctx context.Context) (core.RunOutput, error) {
	out := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
	rows := []Member{}
	controllers, err := discoverControllers(r.deps.Files)
	if err != nil {
		out.Errors = append(out.Errors, v1alpha1.TargetError{Code: v1alpha1.ErrorExecutionFailed, Message: "PCI RAID discovery failed: " + err.Error()})
	}
	configured := toolSourcePaths(loadConfiguredToolSources(r.deps))
	seen := map[string]bool{}
	for _, controller := range controllers {
		backend, binary, _ := discoverBackendTool(r.deps.Processes, controller.Backend, configured)
		if seen[backend] {
			continue
		}
		seen[backend] = true
		var part []Member
		var queryErr error
		args := []string{}
		switch backend {
		case "megacli":
			args = []string{"-LdPdInfo", "-aALL", "-NoLog"}
		case "storcli", "perccli":
			args = []string{"/call", "show", "all", "J"}
		default:
			queryErr = fmt.Errorf("RAID member mapping is not supported for backend %s", backend)
		}
		if queryErr == nil {
			if binary == "" {
				queryErr = fmt.Errorf("%s management tool is unavailable; run toolctl raid doctor", backend)
			} else if !architectureCompatible(binary, r.deps.Architecture) {
				queryErr = fmt.Errorf("%s tool architecture mismatch", backend)
			} else {
				res := r.deps.Processes.Run(ctx, core.ProcessSpec{Path: binary, Args: args, Env: []string{"LC_ALL=C", "LANG=C"}, MaxStdout: 16 << 20, MaxStderr: 1 << 20})
				if res.Err != nil || res.ExitCode != 0 || res.TimedOut || res.StdoutExceeded {
					queryErr = fmt.Errorf("%s member query failed: %s", backend, processMessage(res))
				} else if backend == "megacli" {
					part, queryErr = parseMegaCLIMembers(res.Stdout)
				} else {
					part, queryErr = parseStorCLIMembers(res.Stdout, backend)
				}
			}
		}
		if queryErr != nil {
			out.Errors = append(out.Errors, v1alpha1.TargetError{Code: v1alpha1.ErrorExecutionFailed, Message: queryErr.Error()})
			continue
		}
		rows = append(rows, part...)
	}
	rows = append(rows, collectMDMembers(r.deps.Files)...)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Backend != b.Backend {
			return a.Backend < b.Backend
		}
		if a.Controller != b.Controller {
			return numberLess(a.Controller, b.Controller)
		}
		if a.Volume != b.Volume {
			return numberLess(a.Volume, b.Volume)
		}
		return diskLess(a.Disk, b.Disk)
	})
	for _, m := range rows {
		m.Disk.Controller = m.Controller
		m.Location = physicalDiskLocation(m.Disk)
		m.MegaCLIArgs = megaCLISelector(m.Disk)
		if m.Status == "" {
			m.Status = physicalDiskStatus(m.Disk)
		}
		item, e := resultbuilder.NewItem("RAIDMember", m.Backend+"/"+m.Controller+"/"+m.Volume+"/"+m.Location+"/"+m.ID, "", m)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func numberLess(a, b string) bool {
	ai, ae := strconv.Atoi(a)
	bi, be := strconv.Atoi(b)
	if ae == nil && be == nil {
		return ai < bi
	}
	return a < b
}

// Each LdPdInfo VD section directly contains its physical drives; do not
// infer membership from device IDs, disk order, or slot proximity.
func parseMegaCLIMembers(data []byte) ([]Member, error) {
	rows := []Member{}
	controller, volume := "", ""
	lines := []string{}
	flush := func() error {
		if volume == "" {
			return nil
		}
		inv := newVendorInventory()
		block := []byte("Adapter #" + controller + "\n" + strings.Join(lines, "\n"))
		if err := parseMegaCLIVolumes(block, &inv); err != nil {
			return err
		}
		if err := parseMegaCLIDisks(block, &inv); err != nil {
			return fmt.Errorf("controller %s volume %s: %w", controller, volume, err)
		}
		raid := inv.Volumes[0].RAIDLevel
		spanDepth := 1
		for _, line := range lines {
			key, value, ok := splitVendorField(line)
			if ok && strings.EqualFold(key, "Span Depth") {
				spanDepth, _ = strconv.Atoi(strings.TrimSpace(value))
			}
		}
		if spanDepth > 1 {
			switch raid {
			case "RAID 1":
				raid = "RAID 10"
			case "RAID 5":
				raid = "RAID 50"
			case "RAID 6":
				raid = "RAID 60"
			}
		}
		// PD position reports explicit span/arm information when available.
		positions := []string{}
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "Enclosure Device ID:") {
				positions = append(positions, "")
			}
			if match := megaSpan.FindStringSubmatch(line); len(match) == 2 && len(positions) > 0 {
				positions[len(positions)-1] = match[1]
			}
		}
		for i, d := range inv.Disks {
			span := ""
			if i < len(positions) {
				span = positions[i]
			}
			rows = append(rows, Member{Controller: controller, Volume: volume, RAIDLevel: raid, Span: span, Disk: d})
		}
		return nil
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if match := megaAdapterHeader.FindStringSubmatch(line); len(match) == 2 {
			if e := flush(); e != nil {
				return nil, e
			}
			controller = match[1]
			volume = ""
			lines = nil
			continue
		}
		if match := megaVirtualDrive.FindStringSubmatch(line); len(match) == 2 {
			if e := flush(); e != nil {
				return nil, e
			}
			if controller == "" {
				return nil, fmt.Errorf("virtual drive without adapter ID")
			}
			volume = match[1]
			lines = nil
		}
		if volume != "" {
			lines = append(lines, line)
		}
	}
	if e := flush(); e != nil {
		return nil, e
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("MegaCLI did not report any virtual-drive member mapping")
	}
	return rows, nil
}

var megaSpan = regexp.MustCompile(`(?i)Span:\s*([0-9]+)`)

func parseStorCLIMembers(data []byte, backend string) ([]Member, error) {
	var doc map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if e := decoder.Decode(&doc); e != nil {
		return nil, e
	}
	entries, ok := lookupCase(doc, "Controllers").([]any)
	if !ok {
		return nil, fmt.Errorf("missing Controllers")
	}
	rows := []Member{}
	for _, entry := range entries {
		obj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		status := mapValue(obj, "Command Status")
		if !strings.EqualFold(stringValue(status, "Status"), "Success") {
			return nil, fmt.Errorf("%s controller query failed", backend)
		}
		controller := stringValue(status, "Controller")
		if controller == "" {
			return nil, fmt.Errorf("missing controller ID")
		}
		response := mapValue(obj, "Response Data")
		for _, vd := range rowsValue(response, "VD LIST") {
			group, volume, found := strings.Cut(stringValue(vd, "DG/VD"), "/")
			if !found {
				return nil, fmt.Errorf("missing DG/VD member mapping")
			}
			matched := false
			for _, pd := range rowsValue(response, "PD LIST") {
				if stringValue(pd, "DG") != group {
					continue
				}
				enclosure, slot, found := strings.Cut(stringValue(pd, "EID:Slt"), ":")
				if !found {
					return nil, fmt.Errorf("missing member enclosure/slot")
				}
				d := Disk{Controller: controller, Enclosure: enclosure, Slot: slot, ID: stringValue(pd, "DID"), VendorState: stringValue(pd, "State"), Backend: backend, Model: stringValue(pd, "Model"), SizeBytes: parseVendorSize(stringValue(pd, "Size"))}
				d.State = normalizeHealth(d.VendorState)
				rows = append(rows, Member{Controller: controller, Volume: volume, RAIDLevel: stringValue(vd, "TYPE"), Disk: d})
				matched = true
			}
			if !matched {
				return nil, fmt.Errorf("controller %s volume %s has no reported disk-group members", controller, volume)
			}
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s did not report any virtual-drive member mapping", backend)
	}
	return rows, nil
}

var mdMember = regexp.MustCompile(`([^\s]+)\[([0-9]+)\]((?:\([A-Z]\))*)`)

func collectMDMembers(files core.FileSystem) []Member {
	data, err := files.ReadFile("/proc/mdstat", maxRAIDPseudoFile)
	if err != nil {
		return nil
	}
	rows := []Member{}
	for _, line := range strings.Split(string(data), "\n") {
		left, right, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		level := ""
		for _, field := range strings.Fields(right) {
			if strings.HasPrefix(field, "raid") {
				level = field
				break
			}
		}
		for _, match := range mdMember.FindAllStringSubmatch(right, -1) {
			status := "online"
			if strings.Contains(match[3], "(F)") {
				status = "failed"
			} else if strings.Contains(match[3], "(S)") {
				status = "spare"
			}
			rows = append(rows, Member{Controller: "mdraid", Volume: strings.TrimSpace(left), RAIDLevel: level, Disk: Disk{Controller: "mdraid", ID: "/dev/" + match[1], Status: status, Backend: "mdraid"}})
		}
	}
	return rows
}
