package raid

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/apperror"
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
	args, err := locateArgs(d, action)
	if err != nil {
		return err
	}
	if binary == "" {
		return fmt.Errorf("vendor tool unavailable")
	}
	if !architectureCompatible(binary, r.deps.Architecture) {
		return fmt.Errorf("vendor tool architecture mismatch")
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res := r.deps.Processes.Run(callCtx, core.ProcessSpec{Path: binary, Args: args, Env: []string{"LC_ALL=C", "LANG=C"}, MaxStdout: 1 << 20, MaxStderr: 1 << 20})
	output := strings.ToLower(string(res.Stdout))
	if res.Err != nil || res.ExitCode != 0 || res.TimedOut || res.StdoutExceeded || res.StderrExceeded || strings.Contains(output, "failure") || strings.Contains(output, "failed") || strings.Contains(output, "error:") {
		return fmt.Errorf("locate %s failed: %s", action, processMessage(res))
	}
	confirmed := strings.Contains(output, "success")
	if d.Backend == "megacli" {
		codes := megaExitCode.FindAllStringSubmatch(output, -1)
		confirmed = len(codes) > 0
		for _, code := range codes {
			for _, ch := range code[1] {
				if ch != '0' {
					confirmed = false
				}
			}
		}
	}
	// SSA CLI often returns no output on a successful modify command.
	if d.Backend == "ssacli" && strings.TrimSpace(output) == "" {
		confirmed = true
	}
	if !confirmed {
		return fmt.Errorf("locate %s was not confirmed: %s", action, processMessage(res))
	}
	return nil
}

var ssaDeviceAddress = regexp.MustCompile(`^[0-9]+[A-Za-z]*:[0-9]+:[0-9]+$`)

func locateArgs(d Disk, action string) ([]string, error) {
	if action != "start" && action != "stop" {
		return nil, fmt.Errorf("invalid locate action")
	}
	if !numericDiskAddress.MatchString(d.Controller) {
		return nil, fmt.Errorf("invalid controller ID")
	}
	args := []string{}
	switch d.Backend {
	case "megacli":
		if !validDiskAddress(d) {
			return nil, fmt.Errorf("missing enclosure/slot")
		}
		args = []string{"-PDLocate", "-" + action, "-physdrv[" + d.Enclosure + ":" + d.Slot + "]", "-a" + d.Controller, "-NoLog"}
	case "storcli", "perccli":
		if !validDiskAddress(d) {
			return nil, fmt.Errorf("missing enclosure/slot")
		}
		args = []string{"/c" + d.Controller + "/e" + d.Enclosure + "/s" + d.Slot, action, "locate"}
	case "arcconf":
		channel, device, ok := strings.Cut(d.ID, ":")
		if !ok || !numericDiskAddress.MatchString(channel) || !numericDiskAddress.MatchString(device) {
			return nil, fmt.Errorf("missing arcconf channel/device address")
		}
		args = []string{"IDENTIFY", d.Controller, "DEVICE", channel, device, strings.ToUpper(action), "nologs"}
	case "ssacli":
		if !ssaDeviceAddress.MatchString(d.ID) {
			return nil, fmt.Errorf("invalid SSA physical-drive address")
		}
		led := "on"
		if action == "stop" {
			led = "off"
		}
		args = []string{"ctrl", "slot=" + d.Controller, "pd", d.ID, "modify", "led=" + led}
	default:
		return nil, fmt.Errorf("locate is not supported for backend %s", d.Backend)
	}
	return args, nil
}

// ExecuteStream emits the handoff before waiting, and always attempts cleanup,
// including after interrupted/ambiguous starts and renderer failures.
func (r *Runner) ExecuteStream(ctx context.Context, op v1alpha1.Operation, emit func(core.RunOutput) error) (retErr error) {
	if op.Capability == capabilityTasks {
		return r.streamTasks(ctx, op, emit)
	}
	if op.Capability == capabilityLocate {
		return r.locate(ctx, op, emit)
	}
	service, err := boolOption(op, "service")
	if err != nil || !service || (op.Capability != capabilityDisks && op.Capability != capabilityOverview) {
		return fmt.Errorf("RAID streaming requires --service")
	}
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	selected, e := parseSelection(op)
	if e != nil {
		cancel()
		return e
	}
	inv, warnings := r.enrichedInventory(queryCtx)
	cancel()
	disks := []Disk{}
	for _, d := range inv.Disks {
		if abnormalDisk(d) && selected.disk(d) {
			disks = append(disks, d)
		}
	}
	sort.SliceStable(disks, func(i, j int) bool { return diskLess(disks[i], disks[j]) })
	status := core.RunOutput{}
	completeQuery(&status, inv, selected)
	return r.locateSession(ctx, disks, warnings, status.Errors, 10*time.Minute, false, emit)
}

func (r *Runner) locateSession(ctx context.Context, disks []Disk, warnings []v1alpha1.Diagnostic, sourceErrors []v1alpha1.TargetError, duration time.Duration, stop bool, emit func(core.RunOutput) error) (retErr error) {
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
	operationErrors := append([]v1alpha1.TargetError{}, sourceErrors...)
	output := func() core.RunOutput {
		out := core.RunOutput{Warnings: warnings, Errors: operationErrors}
		for _, d := range disks {
			item, e := resultbuilder.NewItem("RAIDPhysicalDisk", diskKey(d), "", d)
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
				operationErrors = append(operationErrors, v1alpha1.TargetError{Code: v1alpha1.ErrorExecutionFailed, Message: "locator cleanup failed: " + e.Error()})
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
		d.Handoff = fmt.Sprintf("主机 %s，控制器 %s，逻辑盘 %s，阵列类型 %s，背板 %s，厂商槽位 %s；状态=%s；型号/Inquiry=%s；序列号=%s；核对定位灯后由管理员确认更换", host, d.Controller, strings.Join(d.Volumes, ","), strings.Join(d.RAIDLevels, ","), firstNonEmpty(d.Enclosure, "未提供"), firstNonEmpty(d.Slot, "未提供"), d.Status, d.Model, firstNonEmpty(d.Serial, "未提供"))
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
		if d.Status == "missing" && !stop {
			d.Locate = "unavailable-missing"
			d.Handoff += "；磁盘已缺失，无法点灯，需按序列号和历史位置核实"
			warnings = append(warnings, diagnostic("RAID_LOCATE_UNAVAILABLE", "missing disk cannot be located; verify historical location/serial", map[string]string{"disk": diskKey(*d)}))
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		path, _ := discoverTool(r.deps.Processes, d.Backend, configured)
		if stop {
			if e := r.locateDisk(ctx, *d, path, "stop"); e != nil {
				d.Locate = "stop-failed"
				failed = true
				operationErrors = append(operationErrors, v1alpha1.TargetError{Code: v1alpha1.ErrorExecutionFailed, Message: e.Error()})
			} else {
				d.Locate = "off"
			}
			continue
		}
		if _, e := locateArgs(*d, "start"); e == nil && path != "" {
			attempts = append(attempts, attempt{i, path})
		}
		if e := r.locateDisk(ctx, *d, path, "start"); e != nil {
			failed = true
			warnings = append(warnings, diagnostic("RAID_LOCATE_FAILED", e.Error(), map[string]string{"controller": d.Controller, "slot": d.Location}))
		} else {
			d.Locate = "blinking-" + duration.String()
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
		_ = r.deps.Waiter.Wait(ctx, duration)
	} else {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}
	return nil
}

func (r *Runner) locate(ctx context.Context, op v1alpha1.Operation, emit func(core.RunOutput) error) error {
	s, e := parseSelection(op)
	if e != nil {
		return e
	}
	enclosure, e := stringOption(op, "enclosure")
	if e != nil {
		return e
	}
	slot, e := stringOption(op, "slot")
	if e != nil {
		return e
	}
	device, e := stringOption(op, "device")
	if e != nil {
		return e
	}
	if !numericDiskAddress.MatchString(s.controller) || ((device == "") && (enclosure == "" || slot == "")) {
		return apperror.New(v1alpha1.ErrorInvalidArgument, "locate requires --controller and either --enclosure/--slot or --device")
	}
	if (enclosure != "" && !numericDiskAddress.MatchString(enclosure)) || (slot != "" && !numericDiskAddress.MatchString(slot)) {
		return apperror.New(v1alpha1.ErrorInvalidArgument, "enclosure and slot must be numeric")
	}
	stop, e := boolOption(op, "stop")
	if e != nil {
		return e
	}
	duration, e := durationOption(op, "duration", 600000)
	if e != nil {
		return e
	}
	if duration < 1000 || duration > 86400000 {
		return apperror.New(v1alpha1.ErrorInvalidArgument, "duration must be between 1s and 24h")
	}
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	inv, warnings := r.enrichedInventory(qctx)
	cancel()
	disks := []Disk{}
	for _, d := range inv.Disks {
		if s.disk(d) && (device == "" || device == d.ID) && (enclosure == "" || enclosure == d.Enclosure) && (slot == "" || slot == d.Slot) {
			disks = append(disks, d)
		}
	}
	if len(disks) == 0 && stop && s.backend != "" {
		d := Disk{Backend: s.backend, Controller: s.controller, ID: device, Enclosure: enclosure, Slot: slot}
		if _, e := locateArgs(d, "stop"); e == nil {
			disks = append(disks, d)
		}
	}
	if len(disks) != 1 {
		return apperror.New(v1alpha1.ErrorInvalidArgument, fmt.Sprintf("locate requires exactly one disk, matched %d; refine --backend/--controller/--enclosure/--slot/--device", len(disks)))
	}
	if !stop && physicalDiskStatus(disks[0]) == "missing" {
		return apperror.New(v1alpha1.ErrorInvalidArgument, "cannot light a missing disk")
	}
	sourceErrors := inventoryErrors(inv)
	if stop {
		sourceErrors = nil
	}
	return r.locateSession(ctx, disks, warnings, sourceErrors, time.Duration(duration)*time.Millisecond, stop, emit)
}
