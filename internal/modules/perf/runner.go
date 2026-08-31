package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

const (
	maxPseudoFile  = 16 << 20
	maxMetricItems = 64
	maxSampleCount = 100
	minInterval    = 100 * time.Millisecond
	maxInterval    = time.Minute
)

type sampleConfig struct {
	Interval time.Duration
	Count    int
}

type cpuCounters [10]uint64

type diskCounters struct {
	ReadIOs, ReadSectors, ReadMillis    uint64
	WriteIOs, WriteSectors, WriteMillis uint64
	IOMillis, WeightedIOMillis          uint64
}

type networkCounters struct {
	ReceiveBytes, ReceivePackets, ReceiveErrors, ReceiveDrops     uint64
	TransmitBytes, TransmitPackets, TransmitErrors, TransmitDrops uint64
}

type tcpCounters struct {
	OutSegments, RetransmittedSegments uint64
}

func (r *Runner) Execute(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	switch op.Capability {
	case capabilityDoctor:
		return r.doctor()
	case capabilityCPU:
		return r.sampleCPU(ctx, op)
	case capabilityDiskIO:
		return r.sampleDiskIO(ctx, op)
	case capabilityNetwork:
		return r.sampleNetwork(ctx, op)
	case capabilityTCPRetrans:
		return r.sampleTCPRetrans(ctx, op)
	case capabilityHistoryDoctor:
		return r.historyDoctor(ctx)
	case capabilityHistoryCPU:
		return r.historyCPU(ctx, op)
	case capabilityHistoryDiskIO:
		return r.historyDiskIO(ctx, op)
	case capabilityHistoryNetwork:
		return r.historyNetwork(ctx, op)
	case capabilityHistoryTCPRetrans:
		return r.historyTCPRetrans(ctx, op)
	default:
		return core.RunOutput{}, fmt.Errorf("performance module does not support %s", op.Capability)
	}
}

func (r *Runner) ExecuteStream(ctx context.Context, op v1alpha1.Operation, emit func(core.RunOutput) error) error {
	switch op.Capability {
	case capabilityCPU, capabilityDiskIO, capabilityNetwork, capabilityTCPRetrans:
	default:
		return fmt.Errorf("performance capability %s does not support live streaming", op.Capability)
	}
	streamOperation := op
	streamOperation.Options = make(map[string]json.RawMessage, len(op.Options))
	for name, value := range op.Options {
		streamOperation.Options[name] = append(json.RawMessage(nil), value...)
	}
	streamOperation.Options["live"] = json.RawMessage("false")
	streamOperation.Options["count"] = json.RawMessage("1")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		output, err := r.Execute(ctx, streamOperation)
		if err != nil {
			return err
		}
		if err := emit(output); err != nil {
			return err
		}
		if len(output.Errors) > 0 {
			return nil
		}
	}
}

var _ core.StreamingRunner = (*Runner)(nil)

func (r *Runner) doctor() (core.RunOutput, error) {
	sources := map[string]string{}
	warnings := []v1alpha1.Diagnostic{}
	for name, path := range map[string]string{
		"cpu": "/proc/stat", "diskIO": "/proc/diskstats", "network": "/proc/net/dev", "tcp": "/proc/net/snmp",
	} {
		if _, err := r.deps.Files.ReadFile(path, maxPseudoFile); err != nil {
			sources[name] = "unavailable"
			warnings = append(warnings, v1alpha1.Diagnostic{Code: "PERF_SOURCE_UNAVAILABLE", Message: path + " is not readable", Details: map[string]string{"source": name, "reason": err.Error()}})
		} else {
			sources[name] = "available"
		}
	}
	info := DoctorInfo{Ready: len(warnings) == 0, Architecture: r.deps.Architecture, Sources: sources}
	item, err := resultbuilder.NewItem("PerformanceDoctor", "live", "", info)
	if err != nil {
		return core.RunOutput{}, err
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: warnings, Errors: []v1alpha1.TargetError{}}, nil
}

func (r *Runner) sampleCPU(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	config, err := sampleOptions(op)
	if err != nil {
		return invalidOutput(err), nil
	}
	requested, err := stringSliceOption(op, "cpu")
	if err != nil {
		return invalidOutput(err), nil
	}
	previous, err := readCPUCounters(r.deps.Files)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "CPU counters could not be read", err), nil
	}
	available := sortedKeys(previous)
	selected, err := selectNames(requested, available, []string{"cpu"})
	if err != nil {
		return invalidOutput(err), nil
	}
	items := []v1alpha1.Item{}
	for sample := 0; sample < config.Count; sample++ {
		if err := r.deps.Waiter.Wait(ctx, config.Interval); err != nil {
			return core.RunOutput{}, err
		}
		current, err := readCPUCounters(r.deps.Files)
		if err != nil {
			return failedOutput(v1alpha1.ErrorExecutionFailed, "CPU counters could not be read", err), nil
		}
		currentAt := r.deps.Clock.Now()
		for _, cpu := range selected {
			before, beforeOK := previous[cpu]
			after, afterOK := current[cpu]
			if !beforeOK || !afterOK {
				return failedOutput(v1alpha1.ErrorExecutionFailed, "CPU disappeared while sampling", fmt.Errorf("CPU %s is unavailable", cpu)), nil
			}
			stat := calculateCPUStat(before, after, cpu, currentAt)
			item, err := resultbuilder.NewItem("CPUStat", cpu, "", stat)
			if err != nil {
				return core.RunOutput{}, err
			}
			items = append(items, item)
		}
		previous = current
	}
	return successOutput(items), nil
}

func (r *Runner) sampleDiskIO(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	config, err := sampleOptions(op)
	if err != nil {
		return invalidOutput(err), nil
	}
	requested, err := stringSliceOption(op, "device")
	if err != nil {
		return invalidOutput(err), nil
	}
	previous, err := readDiskCounters(r.deps.Files)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "disk IO counters could not be read", err), nil
	}
	_, defaults, err := blockDeviceNames(r.deps.Files)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "block device list could not be read", err), nil
	}
	selected, err := selectNames(requested, sortedKeys(previous), defaults)
	if err != nil {
		return invalidOutput(err), nil
	}
	previousAt := r.deps.Clock.Now()
	items := []v1alpha1.Item{}
	for sample := 0; sample < config.Count; sample++ {
		if err := r.deps.Waiter.Wait(ctx, config.Interval); err != nil {
			return core.RunOutput{}, err
		}
		current, err := readDiskCounters(r.deps.Files)
		if err != nil {
			return failedOutput(v1alpha1.ErrorExecutionFailed, "disk IO counters could not be read", err), nil
		}
		currentAt := r.deps.Clock.Now()
		seconds, err := elapsedSeconds(previousAt, currentAt)
		if err != nil {
			return failedOutput(v1alpha1.ErrorInternal, "sampling clock did not advance", err), nil
		}
		for _, device := range selected {
			before, beforeOK := previous[device]
			after, afterOK := current[device]
			if !beforeOK || !afterOK {
				return failedOutput(v1alpha1.ErrorExecutionFailed, "block device disappeared while sampling", fmt.Errorf("device %s is unavailable", device)), nil
			}
			stat := calculateDiskIOStat(before, after, device, currentAt, seconds)
			item, err := resultbuilder.NewItem("DiskIOStat", device, "", stat)
			if err != nil {
				return core.RunOutput{}, err
			}
			items = append(items, item)
		}
		previous, previousAt = current, currentAt
	}
	return successOutput(items), nil
}

func (r *Runner) sampleNetwork(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	config, err := sampleOptions(op)
	if err != nil {
		return invalidOutput(err), nil
	}
	requested, err := stringSliceOption(op, "interface")
	if err != nil {
		return invalidOutput(err), nil
	}
	previous, err := readNetworkCounters(r.deps.Files)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "network counters could not be read", err), nil
	}
	available := sortedKeys(previous)
	defaults := make([]string, 0, len(available))
	for _, name := range available {
		if name != "lo" {
			defaults = append(defaults, name)
		}
	}
	selected, err := selectNames(requested, available, defaults)
	if err != nil {
		return invalidOutput(err), nil
	}
	previousAt := r.deps.Clock.Now()
	items := []v1alpha1.Item{}
	for sample := 0; sample < config.Count; sample++ {
		if err := r.deps.Waiter.Wait(ctx, config.Interval); err != nil {
			return core.RunOutput{}, err
		}
		current, err := readNetworkCounters(r.deps.Files)
		if err != nil {
			return failedOutput(v1alpha1.ErrorExecutionFailed, "network counters could not be read", err), nil
		}
		currentAt := r.deps.Clock.Now()
		seconds, err := elapsedSeconds(previousAt, currentAt)
		if err != nil {
			return failedOutput(v1alpha1.ErrorInternal, "sampling clock did not advance", err), nil
		}
		for _, name := range selected {
			before, beforeOK := previous[name]
			after, afterOK := current[name]
			if !beforeOK || !afterOK {
				return failedOutput(v1alpha1.ErrorExecutionFailed, "network interface disappeared while sampling", fmt.Errorf("interface %s is unavailable", name)), nil
			}
			stat := calculateNetworkStat(before, after, name, currentAt, seconds)
			item, err := resultbuilder.NewItem("NetworkStat", name, "", stat)
			if err != nil {
				return core.RunOutput{}, err
			}
			items = append(items, item)
		}
		previous, previousAt = current, currentAt
	}
	return successOutput(items), nil
}

func (r *Runner) sampleTCPRetrans(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	config, err := sampleOptions(op)
	if err != nil {
		return invalidOutput(err), nil
	}
	previous, err := readTCPCounters(r.deps.Files)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "TCP counters could not be read", err), nil
	}
	previousAt := r.deps.Clock.Now()
	items := []v1alpha1.Item{}
	for sample := 0; sample < config.Count; sample++ {
		if err := r.deps.Waiter.Wait(ctx, config.Interval); err != nil {
			return core.RunOutput{}, err
		}
		current, err := readTCPCounters(r.deps.Files)
		if err != nil {
			return failedOutput(v1alpha1.ErrorExecutionFailed, "TCP counters could not be read", err), nil
		}
		currentAt := r.deps.Clock.Now()
		seconds, err := elapsedSeconds(previousAt, currentAt)
		if err != nil {
			return failedOutput(v1alpha1.ErrorInternal, "sampling clock did not advance", err), nil
		}
		outSegments := float64(counterDelta(previous.OutSegments, current.OutSegments))
		retransmitted := float64(counterDelta(previous.RetransmittedSegments, current.RetransmittedSegments))
		stat := TCPRetransmissionStat{
			Timestamp: currentAt.Format(time.RFC3339Nano), SegmentsOutPerSecond: outSegments / seconds,
			RetransmittedPerSecond: retransmitted / seconds, RetransmissionPercent: safeRatio(retransmitted*100, outSegments),
		}
		item, err := resultbuilder.NewItem("TCPRetransmissionStat", stat.Timestamp, "", stat)
		if err != nil {
			return core.RunOutput{}, err
		}
		items = append(items, item)
		previous, previousAt = current, currentAt
	}
	return successOutput(items), nil
}

func readCPUCounters(files core.FileSystem) (map[string]cpuCounters, error) {
	data, err := files.ReadFile("/proc/stat", maxPseudoFile)
	if err != nil {
		return nil, err
	}
	result := map[string]cpuCounters{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !(fields[0] == "cpu" || strings.HasPrefix(fields[0], "cpu") && allDigits(strings.TrimPrefix(fields[0], "cpu"))) {
			continue
		}
		var counters cpuCounters
		for index := 0; index < len(counters) && index+1 < len(fields); index++ {
			value, err := strconv.ParseUint(fields[index+1], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse %s counter %d: %w", fields[0], index, err)
			}
			counters[index] = value
		}
		result[fields[0]] = counters
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no CPU counters found")
	}
	return result, nil
}

func readDiskCounters(files core.FileSystem) (map[string]diskCounters, error) {
	data, err := files.ReadFile("/proc/diskstats", maxPseudoFile)
	if err != nil {
		return nil, err
	}
	result := map[string]diskCounters{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 14 || !safeName(fields[2]) {
			continue
		}
		values := make([]uint64, len(fields)-3)
		valid := true
		for index := range values {
			values[index], err = strconv.ParseUint(fields[index+3], 10, 64)
			if err != nil {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		result[fields[2]] = diskCounters{
			ReadIOs: values[0], ReadSectors: values[2], ReadMillis: values[3],
			WriteIOs: values[4], WriteSectors: values[6], WriteMillis: values[7],
			IOMillis: values[9], WeightedIOMillis: values[10],
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no disk IO counters found")
	}
	return result, nil
}

func readNetworkCounters(files core.FileSystem) (map[string]networkCounters, error) {
	data, err := files.ReadFile("/proc/net/dev", maxPseudoFile)
	if err != nil {
		return nil, err
	}
	result := map[string]networkCounters{}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if !safeName(name) || len(fields) < 16 {
			continue
		}
		values := make([]uint64, 16)
		valid := true
		for index := range values {
			values[index], err = strconv.ParseUint(fields[index], 10, 64)
			if err != nil {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		result[name] = networkCounters{
			ReceiveBytes: values[0], ReceivePackets: values[1], ReceiveErrors: values[2], ReceiveDrops: values[3],
			TransmitBytes: values[8], TransmitPackets: values[9], TransmitErrors: values[10], TransmitDrops: values[11],
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no network counters found")
	}
	return result, nil
}

func readTCPCounters(files core.FileSystem) (tcpCounters, error) {
	data, err := files.ReadFile("/proc/net/snmp", maxPseudoFile)
	if err != nil {
		return tcpCounters{}, err
	}
	lines := strings.Split(string(data), "\n")
	for index := 0; index < len(lines); index++ {
		headings := strings.Fields(lines[index])
		if len(headings) < 2 || headings[0] != "Tcp:" {
			continue
		}
		valueIndex := index + 1
		for valueIndex < len(lines) && strings.TrimSpace(lines[valueIndex]) == "" {
			valueIndex++
		}
		if valueIndex >= len(lines) {
			return tcpCounters{}, fmt.Errorf("TCP values row is missing")
		}
		values := strings.Fields(lines[valueIndex])
		if len(values) < 2 || values[0] != "Tcp:" {
			return tcpCounters{}, fmt.Errorf("TCP values row is missing after headings")
		}
		indexes := map[string]int{}
		for fieldIndex, name := range headings {
			indexes[name] = fieldIndex
		}
		parseCounter := func(name string) (uint64, error) {
			fieldIndex, ok := indexes[name]
			if !ok {
				return 0, fmt.Errorf("TCP field %s is missing", name)
			}
			if fieldIndex >= len(values) {
				return 0, fmt.Errorf("TCP field %s has no value", name)
			}
			value, err := strconv.ParseUint(values[fieldIndex], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse TCP field %s: %w", name, err)
			}
			return value, nil
		}
		out, err := parseCounter("OutSegs")
		if err != nil {
			return tcpCounters{}, err
		}
		retrans, err := parseCounter("RetransSegs")
		if err != nil {
			return tcpCounters{}, err
		}
		return tcpCounters{OutSegments: out, RetransmittedSegments: retrans}, nil
	}
	return tcpCounters{}, fmt.Errorf("TCP counters not found")
}

func calculateCPUStat(before, after cpuCounters, cpu string, at time.Time) CPUStat {
	delta := cpuCounters{}
	for index := range delta {
		delta[index] = counterDelta(before[index], after[index])
	}
	total := uint64(0)
	for index := 0; index < 8; index++ {
		total += delta[index]
	}
	return CPUStat{
		Timestamp: at.Format(time.RFC3339Nano), CPU: cpu,
		UserPercent: ratioPercent(delta[0], total), SystemPercent: ratioPercent(delta[2], total),
		IOWaitPercent: ratioPercent(delta[4], total), IRQPercent: ratioPercent(delta[5], total),
		SoftIRQPercent: ratioPercent(delta[6], total), IdlePercent: ratioPercent(delta[3], total),
		StealPercent: ratioPercent(delta[7], total), GuestPercent: ratioPercent(delta[8], total),
		UtilPercent: ratioPercent(total-delta[3]-delta[4]-delta[7], total),
	}
}

func calculateDiskIOStat(before, after diskCounters, device string, at time.Time, seconds float64) DiskIOStat {
	readIOs := float64(counterDelta(before.ReadIOs, after.ReadIOs))
	writeIOs := float64(counterDelta(before.WriteIOs, after.WriteIOs))
	readSectors := float64(counterDelta(before.ReadSectors, after.ReadSectors))
	writeSectors := float64(counterDelta(before.WriteSectors, after.WriteSectors))
	readMillis := float64(counterDelta(before.ReadMillis, after.ReadMillis))
	writeMillis := float64(counterDelta(before.WriteMillis, after.WriteMillis))
	ioMillis := float64(counterDelta(before.IOMillis, after.IOMillis))
	weightedMillis := float64(counterDelta(before.WeightedIOMillis, after.WeightedIOMillis))
	totalIOs := readIOs + writeIOs
	return DiskIOStat{
		Timestamp: at.Format(time.RFC3339Nano), Device: device,
		ReadsPerSecond: readIOs / seconds, WritesPerSecond: writeIOs / seconds,
		ReadBytesPerSecond: readSectors * 512 / seconds, WriteBytesPerSecond: writeSectors * 512 / seconds,
		AverageRequestBytes: safeRatio((readSectors+writeSectors)*512, totalIOs),
		AverageWaitMillis:   safeRatio(readMillis+writeMillis, totalIOs), ReadWaitMillis: safeRatio(readMillis, readIOs),
		WriteWaitMillis: safeRatio(writeMillis, writeIOs), AverageQueueDepth: weightedMillis / (seconds * 1000),
		UtilizationPercent: min(ioMillis/(seconds*10), 100),
	}
}

func calculateNetworkStat(before, after networkCounters, name string, at time.Time, seconds float64) NetworkStat {
	return NetworkStat{
		Timestamp: at.Format(time.RFC3339Nano), Interface: name,
		ReceiveBytesPerSec:    float64(counterDelta(before.ReceiveBytes, after.ReceiveBytes)) / seconds,
		TransmitBytesPerSec:   float64(counterDelta(before.TransmitBytes, after.TransmitBytes)) / seconds,
		ReceivePacketsPerSec:  float64(counterDelta(before.ReceivePackets, after.ReceivePackets)) / seconds,
		TransmitPacketsPerSec: float64(counterDelta(before.TransmitPackets, after.TransmitPackets)) / seconds,
		ErrorsPerSecond:       float64(counterDelta(before.ReceiveErrors, after.ReceiveErrors)+counterDelta(before.TransmitErrors, after.TransmitErrors)) / seconds,
		DropsPerSecond:        float64(counterDelta(before.ReceiveDrops, after.ReceiveDrops)+counterDelta(before.TransmitDrops, after.TransmitDrops)) / seconds,
	}
}

func sampleOptions(op v1alpha1.Operation) (sampleConfig, error) {
	interval, err := durationOption(op, "interval")
	if err != nil {
		return sampleConfig{}, err
	}
	count, err := intOption(op, "count")
	if err != nil {
		return sampleConfig{}, err
	}
	if interval < minInterval || interval > maxInterval {
		return sampleConfig{}, fmt.Errorf("--interval must be between %s and %s", minInterval, maxInterval)
	}
	if count < 1 || count > maxSampleCount {
		return sampleConfig{}, fmt.Errorf("--count must be between 1 and %d", maxSampleCount)
	}
	return sampleConfig{Interval: interval, Count: int(count)}, nil
}

func durationOption(op v1alpha1.Operation, name string) (time.Duration, error) {
	raw, ok := op.Options[name]
	if !ok {
		return 0, fmt.Errorf("--%s is required", name)
	}
	var milliseconds int64
	if err := json.Unmarshal(raw, &milliseconds); err != nil {
		return 0, fmt.Errorf("--%s must be a duration", name)
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}

func intOption(op v1alpha1.Operation, name string) (int64, error) {
	raw, ok := op.Options[name]
	if !ok {
		return 0, fmt.Errorf("--%s is required", name)
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("--%s must be an integer", name)
	}
	return value, nil
}

func stringSliceOption(op v1alpha1.Operation, name string) ([]string, error) {
	raw, ok := op.Options[name]
	if !ok {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("--%s must be a list", name)
	}
	return values, nil
}

func blockDeviceNames(files core.FileSystem) ([]string, []string, error) {
	entries, err := files.ReadDir("/sys/class/block")
	if err != nil {
		return nil, nil, err
	}
	available, defaults := []string{}, []string{}
	for _, entry := range entries {
		name := entry.Name()
		if !safeName(name) {
			continue
		}
		available = append(available, name)
		_, partitionErr := files.Stat("/sys/class/block/" + name + "/partition")
		if partitionErr != nil && !strings.HasPrefix(name, "loop") && !strings.HasPrefix(name, "ram") && !strings.HasPrefix(name, "zram") {
			defaults = append(defaults, name)
		}
	}
	sort.Strings(available)
	sort.Strings(defaults)
	return available, defaults, nil
}

func selectNames(requested, available, defaults []string) ([]string, error) {
	if len(requested) == 0 {
		requested = defaults
	}
	if len(requested) == 0 {
		return nil, fmt.Errorf("no matching resources were found")
	}
	if len(requested) > maxMetricItems {
		return nil, fmt.Errorf("at most %d resources can be queried", maxMetricItems)
	}
	known := map[string]bool{}
	for _, name := range available {
		known[name] = true
	}
	seen := map[string]bool{}
	selected := []string{}
	for _, name := range requested {
		if !known[name] {
			return nil, fmt.Errorf("resource %q does not exist on this host", name)
		}
		if !seen[name] {
			selected = append(selected, name)
			seen[name] = true
		}
	}
	return selected, nil
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func safeName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("_.:-", character)) {
			return false
		}
	}
	return true
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func counterDelta(before, after uint64) uint64 {
	if after < before {
		return 0
	}
	return after - before
}

func ratioPercent(value, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(value) * 100 / float64(total)
}

func safeRatio(numerator, denominator float64) float64 {
	if denominator == 0 {
		return 0
	}
	return numerator / denominator
}

func elapsedSeconds(before, after time.Time) (float64, error) {
	seconds := after.Sub(before).Seconds()
	if seconds <= 0 {
		return 0, fmt.Errorf("elapsed time is %s", after.Sub(before))
	}
	return seconds, nil
}

func successOutput(items []v1alpha1.Item) core.RunOutput {
	return core.RunOutput{Items: items, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
}

func invalidOutput(err error) core.RunOutput {
	return failedOutput(v1alpha1.ErrorInvalidArgument, err.Error(), err)
}

func failedOutput(code v1alpha1.ErrorCode, message string, err error) core.RunOutput {
	details := map[string]string{}
	if err != nil {
		details["reason"] = err.Error()
	}
	return core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{{Code: code, Message: message, Details: details}}}
}
