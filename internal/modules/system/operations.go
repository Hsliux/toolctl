package system

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

type HealthCheck struct {
	Check   string `json:"check"`
	Status  string `json:"status"`
	Value   string `json:"value"`
	Message string `json:"message"`
}

type ProcessMetric struct {
	PID                 int     `json:"pid"`
	UID                 int     `json:"uid"`
	Command             string  `json:"command"`
	CPUPercent          float64 `json:"cpuPercent"`
	MemoryBytes         uint64  `json:"memoryBytes"`
	ReadBytesPerSecond  uint64  `json:"readBytesPerSecond"`
	WriteBytesPerSecond uint64  `json:"writeBytesPerSecond"`
}

type DiskHealth struct {
	Device             string  `json:"device"`
	Status             string  `json:"status"`
	State              string  `json:"state,omitempty"`
	ReadOnly           bool    `json:"readOnly"`
	Scheduler          string  `json:"scheduler,omitempty"`
	QueueDepth         int64   `json:"queueDepth,omitempty"`
	TimeoutMS          int64   `json:"timeoutMs,omitempty"`
	SmartStatus        string  `json:"smartStatus,omitempty"`
	TemperatureCelsius float64 `json:"temperatureCelsius,omitempty"`
	PercentageUsed     float64 `json:"percentageUsed,omitempty"`
	MediaErrors        uint64  `json:"mediaErrors,omitempty"`
	HealthTool         string  `json:"healthTool,omitempty"`
}

func (r *Runner) runHealth() (core.RunOutput, error) {
	checks := []HealthCheck{}
	loadText, loadErr := readTrimmed(r.deps.Files, "/proc/loadavg")
	if loadErr == nil {
		fields := strings.Fields(loadText)
		load, _ := strconv.ParseFloat(firstField(fields), 64)
		perCPU := load / float64(maxInt(r.deps.LogicalCPUs, 1))
		status := thresholdStatus(perCPU, 1, 2)
		checks = append(checks, HealthCheck{"load", status, fmt.Sprintf("%.2f (%.2f/cpu)", load, perCPU), "1-minute load normalized by visible CPUs"})
	} else {
		checks = append(checks, HealthCheck{"load", "unknown", "", loadErr.Error()})
	}
	memory, memoryErr := collectMemory(r.deps.Files)
	if memoryErr == nil && memory.TotalBytes != nil && memory.AvailableBytes != nil && *memory.TotalBytes > 0 {
		available := float64(*memory.AvailableBytes) / float64(*memory.TotalBytes) * 100
		status := "pass"
		if available < 5 {
			status = "fail"
		} else if available < 10 {
			status = "warn"
		}
		checks = append(checks, HealthCheck{"memory", status, fmt.Sprintf("%.1f%% available", available), "available memory"})
	} else {
		checks = append(checks, HealthCheck{"memory", "unknown", "", "memory availability could not be read"})
	}
	if data, err := r.deps.Files.ReadFile("/proc/meminfo", maxPseudoFile); err == nil {
		values := meminfoValues(string(data))
		used := uint64(0)
		if values["SwapTotal"] > values["SwapFree"] {
			used = values["SwapTotal"] - values["SwapFree"]
		}
		status := "pass"
		if used > 0 {
			status = "warn"
		}
		checks = append(checks, HealthCheck{"swap", status, formatBytesSimple(used), "used swap"})
	}
	if stats, err := r.deps.Files.StatFS("/"); err == nil && stats.TotalBytes > 0 {
		usedPercent := float64(stats.TotalBytes-stats.AvailableBytes) / float64(stats.TotalBytes) * 100
		checks = append(checks, HealthCheck{"root-filesystem", thresholdStatus(usedPercent, 85, 95), fmt.Sprintf("%.1f%% used", usedPercent), "root filesystem capacity"})
	}
	if interfaces, err := collectNetworkInterfaces(r.deps.Files); err == nil {
		down := 0
		for _, item := range interfaces {
			if item.Kind != "loopback" && item.State == "down" {
				down++
			}
		}
		status := "pass"
		if down > 0 {
			status = "warn"
		}
		checks = append(checks, HealthCheck{"network", status, fmt.Sprintf("%d down", down), "non-loopback interfaces in down state"})
	}
	return healthOutput(checks)
}

func healthOutput(checks []HealthCheck) (core.RunOutput, error) {
	output := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
	for _, check := range checks {
		item, err := resultbuilder.NewItem("HealthCheck", check.Check, check.Check, check)
		if err != nil {
			return core.RunOutput{}, err
		}
		output.Items = append(output.Items, item)
		if check.Status == "warn" {
			output.Warnings = append(output.Warnings, v1alpha1.Diagnostic{Code: "HEALTH_WARNING", Message: check.Message, Details: map[string]string{"check": check.Check, "value": check.Value}})
		}
		if check.Status == "fail" {
			output.Errors = append(output.Errors, v1alpha1.TargetError{Target: check.Check, Code: v1alpha1.ErrorExecutionFailed, Message: check.Message, Details: map[string]string{"value": check.Value}})
		}
	}
	return output, nil
}

type processSnapshot struct {
	ticks, readBytes, writeBytes, memory uint64
	uid                                  int
	command                              string
}

func (r *Runner) runTop(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	mode := strings.ToLower(strings.TrimSpace(op.Name))
	if mode == "" {
		mode = "cpu"
	}
	if mode != "cpu" && mode != "mem" && mode != "io" {
		return failedOutput(v1alpha1.ErrorInvalidArgument, "top mode must be cpu, mem, or io", fmt.Errorf("invalid mode %q", mode)), nil
	}
	intervalMS, limit := int64(1000), int64(10)
	if raw, ok := op.Options["interval"]; ok {
		if err := json.Unmarshal(raw, &intervalMS); err != nil {
			return failedOutput(v1alpha1.ErrorInvalidArgument, "invalid interval", err), nil
		}
	}
	if raw, ok := op.Options["limit"]; ok {
		if err := json.Unmarshal(raw, &limit); err != nil {
			return failedOutput(v1alpha1.ErrorInvalidArgument, "invalid limit", err), nil
		}
	}
	if intervalMS < 100 || intervalMS > 60000 || limit < 1 || limit > 1000 {
		return failedOutput(v1alpha1.ErrorInvalidArgument, "interval must be 100ms-1m and limit must be 1-1000", fmt.Errorf("invalid bounds")), nil
	}
	first, totalFirst, err := collectProcesses(r.deps.Files)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "process snapshot could not be read", err), nil
	}
	interval := time.Duration(intervalMS) * time.Millisecond
	if r.deps.Waiter != nil {
		if err := r.deps.Waiter.Wait(ctx, interval); err != nil {
			return core.RunOutput{}, err
		}
	} else {
		select {
		case <-ctx.Done():
			return core.RunOutput{}, ctx.Err()
		case <-time.After(interval):
		}
	}
	second, totalSecond, err := collectProcesses(r.deps.Files)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "process snapshot could not be read", err), nil
	}
	totalDelta := uint64(0)
	if totalSecond >= totalFirst {
		totalDelta = totalSecond - totalFirst
	}
	results := []ProcessMetric{}
	for pid, current := range second {
		previous, ok := first[pid]
		if !ok {
			continue
		}
		metric := ProcessMetric{PID: pid, UID: current.uid, Command: current.command, MemoryBytes: current.memory}
		if totalDelta > 0 && current.ticks >= previous.ticks {
			metric.CPUPercent = float64(current.ticks-previous.ticks) / float64(totalDelta) * float64(maxInt(r.deps.LogicalCPUs, 1)) * 100
		}
		if current.readBytes >= previous.readBytes {
			metric.ReadBytesPerSecond = uint64(float64(current.readBytes-previous.readBytes) / interval.Seconds())
		}
		if current.writeBytes >= previous.writeBytes {
			metric.WriteBytesPerSecond = uint64(float64(current.writeBytes-previous.writeBytes) / interval.Seconds())
		}
		results = append(results, metric)
	}
	sort.Slice(results, func(i, j int) bool {
		switch mode {
		case "mem":
			return results[i].MemoryBytes > results[j].MemoryBytes
		case "io":
			return results[i].ReadBytesPerSecond+results[i].WriteBytesPerSecond > results[j].ReadBytesPerSecond+results[j].WriteBytesPerSecond
		default:
			return results[i].CPUPercent > results[j].CPUPercent
		}
	})
	if int64(len(results)) > limit {
		results = results[:limit]
	}
	output := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
	for _, metric := range results {
		item, err := resultbuilder.NewItem("ProcessMetric", strconv.Itoa(metric.PID), "", metric)
		if err != nil {
			return core.RunOutput{}, err
		}
		output.Items = append(output.Items, item)
	}
	return output, nil
}

func collectProcesses(files core.FileSystem) (map[int]processSnapshot, uint64, error) {
	stat, err := files.ReadFile("/proc/stat", maxPseudoFile)
	if err != nil {
		return nil, 0, err
	}
	total := uint64(0)
	lines := strings.Split(string(stat), "\n")
	if len(lines) > 0 {
		fields := strings.Fields(lines[0])
		if len(fields) == 0 || fields[0] != "cpu" {
			return nil, 0, fmt.Errorf("aggregate CPU counters are missing")
		}
		for _, field := range fields[1:] {
			value, _ := strconv.ParseUint(field, 10, 64)
			total += value
		}
	}
	entries, err := files.ReadDir("/proc")
	if err != nil {
		return nil, 0, err
	}
	result := map[int]processSnapshot{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		snap, err := readProcess(files, pid)
		if err == nil {
			result[pid] = snap
		}
	}
	return result, total, nil
}

func readProcess(files core.FileSystem, pid int) (processSnapshot, error) {
	base := "/proc/" + strconv.Itoa(pid)
	data, err := files.ReadFile(base+"/stat", 64<<10)
	if err != nil {
		return processSnapshot{}, err
	}
	text := string(data)
	closeIndex := strings.LastIndex(text, ")")
	openIndex := strings.Index(text, "(")
	if openIndex < 0 || closeIndex < openIndex {
		return processSnapshot{}, fmt.Errorf("invalid stat")
	}
	fields := strings.Fields(text[closeIndex+1:])
	if len(fields) < 22 {
		return processSnapshot{}, fmt.Errorf("short stat")
	}
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	rss, _ := strconv.ParseInt(fields[21], 10, 64)
	snap := processSnapshot{ticks: utime + stime, command: text[openIndex+1 : closeIndex]}
	if rss > 0 {
		snap.memory = uint64(rss) * uint64(os.Getpagesize())
	}
	if status, err := files.ReadFile(base+"/status", 1<<20); err == nil {
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "Uid:") {
				parts := strings.Fields(line)
				if len(parts) > 1 {
					snap.uid, _ = strconv.Atoi(parts[1])
				}
				break
			}
		}
	}
	if ioData, err := files.ReadFile(base+"/io", 1<<20); err == nil {
		for _, line := range strings.Split(string(ioData), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			parsed, _ := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if key == "read_bytes" {
				snap.readBytes = parsed
			} else if key == "write_bytes" {
				snap.writeBytes = parsed
			}
		}
	}
	return snap, nil
}

func (r *Runner) runDiskHealth(ctx context.Context) (core.RunOutput, error) {
	devices, err := collectBlockDevices(r.deps.Files)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "block devices could not be read", err), nil
	}
	output := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
	for _, device := range topLevelDisks(devices) {
		base := "/sys/class/block/" + device.Name + "/"
		ro, _ := readTrimmed(r.deps.Files, base+"ro")
		state, _ := readTrimmed(r.deps.Files, base+"device/state")
		scheduler, _ := readTrimmed(r.deps.Files, base+"queue/scheduler")
		queue, _ := readInt64(r.deps.Files, base+"queue/nr_requests")
		timeout, _ := readInt64(r.deps.Files, base+"device/timeout")
		health := DiskHealth{Device: device.Name, Status: "pass", State: state, ReadOnly: ro == "1", Scheduler: scheduler, QueueDepth: queue, TimeoutMS: timeout * 1000}
		if health.ReadOnly || state != "" && state != "running" && state != "live" {
			health.Status = "warn"
		}
		if r.deps.Processes != nil {
			if deepErr := enrichDiskHealth(ctx, r.deps.Processes, &health); deepErr != nil {
				output.Warnings = append(output.Warnings, v1alpha1.Diagnostic{Code: "DISK_DEEP_HEALTH_UNAVAILABLE", Message: "optional deep disk health could not be read", Details: map[string]string{"device": device.Name, "reason": deepErr.Error()}})
			}
		}
		if health.SmartStatus == "failed" || health.SmartStatus == "critical" {
			health.Status = "fail"
		} else if health.MediaErrors > 0 && health.Status == "pass" {
			health.Status = "warn"
		}
		item, e := resultbuilder.NewItem("DiskHealth", device.Name, "", health)
		if e != nil {
			return core.RunOutput{}, e
		}
		output.Items = append(output.Items, item)
		if health.Status == "warn" {
			output.Warnings = append(output.Warnings, v1alpha1.Diagnostic{Code: "DISK_HEALTH_WARNING", Message: "block device state requires attention", Details: map[string]string{"device": device.Name, "state": state}})
		}
		if health.Status == "fail" {
			output.Errors = append(output.Errors, v1alpha1.TargetError{Target: device.Name, Code: v1alpha1.ErrorExecutionFailed, Message: "disk health check failed", Details: map[string]string{"device": device.Name, "smartStatus": health.SmartStatus}})
		}
	}
	return output, nil
}

func enrichDiskHealth(ctx context.Context, processes core.ProcessExecutor, health *DiskHealth) error {
	device := "/dev/" + health.Device
	attempts := []string{}
	if tool, err := processes.LookPath("smartctl"); err == nil {
		result := processes.Run(ctx, core.ProcessSpec{Path: tool, Args: []string{"-j", "-H", "-A", device}, MaxStdout: 2 << 20, MaxStderr: 256 << 10})
		if len(result.Stdout) > 0 && parseSmartctlHealth(result.Stdout, health) == nil {
			health.HealthTool = tool
			return nil
		}
		attempts = append(attempts, "smartctl did not return supported health JSON")
	}
	if strings.HasPrefix(health.Device, "nvme") {
		if tool, err := processes.LookPath("nvme"); err == nil {
			result := processes.Run(ctx, core.ProcessSpec{Path: tool, Args: []string{"smart-log", "-o", "json", device}, MaxStdout: 2 << 20, MaxStderr: 256 << 10})
			if result.Err == nil && result.ExitCode == 0 && parseNVMeHealth(result.Stdout, health) == nil {
				health.HealthTool = tool
				return nil
			}
			attempts = append(attempts, "nvme did not return supported health JSON")
		}
	}
	if len(attempts) > 0 {
		return fmt.Errorf("%s", strings.Join(attempts, "; "))
	}
	return nil
}

func parseSmartctlHealth(data []byte, health *DiskHealth) error {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	recognized := false
	if smart, ok := value["smart_status"].(map[string]any); ok {
		if passed, ok := smart["passed"].(bool); ok {
			recognized = true
			if passed {
				health.SmartStatus = "passed"
			} else {
				health.SmartStatus = "failed"
			}
		}
	}
	if temperature, ok := value["temperature"].(map[string]any); ok {
		recognized = true
		health.TemperatureCelsius = numberValue(temperature["current"])
	}
	if nvme, ok := value["nvme_smart_health_information_log"].(map[string]any); ok {
		recognized = true
		critical := uint64(numberValue(nvme["critical_warning"]))
		health.PercentageUsed = numberValue(nvme["percentage_used"])
		health.MediaErrors = uint64(numberValue(nvme["media_errors"]))
		if health.TemperatureCelsius == 0 {
			health.TemperatureCelsius = numberValue(nvme["temperature"])
		}
		if critical > 0 {
			health.SmartStatus = "critical"
		}
	}
	if !recognized {
		return fmt.Errorf("SMART JSON contains no supported health fields")
	}
	return nil
}
func parseNVMeHealth(data []byte, health *DiskHealth) error {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	critical := uint64(numberValue(value["critical_warning"]))
	health.TemperatureCelsius = numberValue(value["temperature"])
	health.PercentageUsed = numberValue(value["percentage_used"])
	health.MediaErrors = uint64(numberValue(value["media_errors"]))
	if critical > 0 {
		health.SmartStatus = "critical"
	} else {
		health.SmartStatus = "passed"
	}
	return nil
}
func numberValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case string:
		parsed, _ := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed
	default:
		return 0
	}
}

func readInt64(files core.FileSystem, path string) (int64, error) {
	value, err := readTrimmed(files, path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(value, 10, 64)
}
func meminfoValues(content string) map[string]uint64 {
	result := map[string]uint64{}
	for _, line := range strings.Split(content, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		number, _ := strconv.ParseUint(fields[0], 10, 64)
		result[key] = number * 1024
	}
	return result
}
func thresholdStatus(value, warn, fail float64) string {
	if value >= fail {
		return "fail"
	}
	if value >= warn {
		return "warn"
	}
	return "pass"
}
func firstField(values []string) string {
	if len(values) == 0 {
		return "0"
	}
	return values[0]
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func formatBytesSimple(value uint64) string {
	const mib = 1 << 20
	return fmt.Sprintf("%.1f MiB", float64(value)/mib)
}
