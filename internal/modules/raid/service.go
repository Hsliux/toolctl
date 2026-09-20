package raid

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

var numericDiskAddress = regexp.MustCompile(`^[0-9]+$`)

func validDiskAddress(d Disk) bool {
	return numericDiskAddress.MatchString(d.Controller) && numericDiskAddress.MatchString(d.Enclosure) && numericDiskAddress.MatchString(d.Slot)
}

func megaCLISelector(d Disk) string {
	if d.Backend != "megacli" || !validDiskAddress(d) {
		return ""
	}
	return fmt.Sprintf("-physdrv '[%s:%s]' -a%s", d.Enclosure, d.Slot, d.Controller)
}

func abnormalDisk(d Disk) bool {
	if d.PredictiveFailure != nil && *d.PredictiveFailure {
		return true
	}
	switch physicalDiskStatus(d) {
	case "failed", "offline", "missing", "unconfigured-bad", "rebuilding", "degraded", "unknown":
		return true
	}
	return false
}

func (r *Runner) locateDisk(ctx context.Context, d Disk, binary, action string) error {
	if !validDiskAddress(d) {
		return fmt.Errorf("incomplete or invalid controller/enclosure/slot; cannot locate")
	}
	args := []string{}
	switch d.Backend {
	case "megacli":
		args = []string{"-PDLocate", "-" + action, "-physdrv[" + d.Enclosure + ":" + d.Slot + "]", "-a" + d.Controller, "-NoLog"}
	case "storcli", "perccli":
		args = []string{"/c" + d.Controller + "/e" + d.Enclosure + "/s" + d.Slot, action, "locate"}
	default:
		return fmt.Errorf("locate is not supported for backend %s", d.Backend)
	}
	if binary == "" {
		return fmt.Errorf("vendor tool unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res := r.deps.Processes.Run(callCtx, core.ProcessSpec{Path: binary, Args: args, Env: []string{"LC_ALL=C", "LANG=C"}, MaxStdout: 1 << 20, MaxStderr: 1 << 20})
	output := strings.ToLower(string(res.Stdout))
	if res.Err != nil || res.ExitCode != 0 || res.TimedOut || res.StdoutExceeded || strings.Contains(output, "failure") || strings.Contains(output, "failed") {
		return fmt.Errorf("locate %s failed: %s", action, processMessage(res))
	}
	confirmed := strings.Contains(output, "success")
	if d.Backend == "megacli" {
		confirmed = strings.Contains(output, "exit code: 0x00")
	}
	if !confirmed {
		return fmt.Errorf("locate %s was not confirmed: %s", action, processMessage(res))
	}
	return nil
}

// ExecuteStream emits the handoff before waiting, and always attempts cleanup,
// including after interrupted/ambiguous starts and renderer failures.
func (r *Runner) ExecuteStream(ctx context.Context, op v1alpha1.Operation, emit func(core.RunOutput) error) (retErr error) {
	service, err := boolOption(op, "service")
	if err != nil || !service || (op.Capability != capabilityDisks && op.Capability != capabilityOverview) {
		return fmt.Errorf("RAID streaming requires --service")
	}
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	inv, warnings := r.inventory(queryCtx)
	cancel()
	disks := []Disk{}
	for _, d := range inv.Disks {
		if abnormalDisk(d) {
			disks = append(disks, d)
		}
	}
	sort.SliceStable(disks, func(i, j int) bool { return diskLess(disks[i], disks[j]) })
	configured := toolSourcePaths(loadConfiguredToolSources(r.deps))
	host := "unknown"
	if r.deps.Hostname != nil {
		if h, e := r.deps.Hostname(); e == nil {
			host = h
		}
	}
	type attempt struct {
		index int
		path  string
	}
	attempts := []attempt{}
	lit := 0
	failed := false
	output := func() core.RunOutput {
		out := core.RunOutput{Warnings: warnings}
		for _, d := range disks {
			item, e := resultbuilder.NewItem("RAIDPhysicalDisk", d.Backend+"/"+d.Controller+"/"+d.Location, "", d)
			if e == nil {
				out.Items = append(out.Items, item)
			}
		}
		return out
	}
	defer func() {
		for _, a := range attempts {
			d := &disks[a.index]
			if e := r.locateDisk(context.Background(), *d, a.path, "stop"); e != nil {
				d.Locate = "stop-failed"
				warnings = append(warnings, diagnostic("RAID_LOCATE_STOP_FAILED", e.Error(), map[string]string{"controller": d.Controller, "slot": d.Location, "tool": a.path}))
				failed = true
			} else {
				d.Locate = "off"
			}
		}
		if len(attempts) > 0 {
			if e := emit(output()); retErr == nil {
				retErr = e
			}
		}
		if failed && retErr == nil {
			retErr = fmt.Errorf("some RAID locate operations failed; review warnings")
		}
	}()
	for i := range disks {
		d := &disks[i]
		d.Status, d.Location, d.Host = physicalDiskStatus(*d), physicalDiskLocation(*d), host
		d.Handoff = fmt.Sprintf("主机 %s，控制器 %s，背板 %s，厂商槽位 %s；型号/Inquiry=%s；序列号=%s；核对定位灯后由管理员确认更换", host, d.Controller, d.Enclosure, d.Slot, d.Model, firstNonEmpty(d.Serial, "未提供"))
		if d.PredictiveFailure != nil && *d.PredictiveFailure {
			d.Handoff += "；预测故障告警"
		}
		if d.Status == "rebuilding" {
			d.Handoff += "；重建中勿拔盘"
		}
		if d.Status == "unknown" {
			d.Handoff += "；状态未知，需核实"
		}
		d.Locate = "unavailable"
		if ctx.Err() != nil {
			return ctx.Err()
		}
		path, _ := discoverTool(r.deps.Processes, d.Backend, configured)
		if validDiskAddress(*d) && path != "" && (d.Backend == "megacli" || d.Backend == "storcli" || d.Backend == "perccli") {
			attempts = append(attempts, attempt{i, path})
		}
		if e := r.locateDisk(ctx, *d, path, "start"); e != nil {
			failed = true
			warnings = append(warnings, diagnostic("RAID_LOCATE_FAILED", e.Error(), map[string]string{"controller": d.Controller, "slot": d.Location}))
		} else {
			d.Locate = "blinking-10m"
			lit++
		}
	}
	if len(disks) == 0 {
		warnings = append(warnings, diagnostic("RAID_SERVICE", "no abnormal physical disks found in collected inventory", nil))
	}
	if err := emit(output()); err != nil {
		return err
	}
	if lit == 0 {
		return nil
	}
	// Keep the foreground process alive so normal cancellation can stop LEDs.
	if r.deps.Waiter != nil {
		_ = r.deps.Waiter.Wait(ctx, 10*time.Minute)
	} else {
		timer := time.NewTimer(10 * time.Minute)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}
	return nil
}
