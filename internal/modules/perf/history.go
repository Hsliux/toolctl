package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

const (
	historyArchivePath = "/var/log/sre_proc"
	maxHistoryOutput   = 16 << 20
)

func (r *Runner) historyDoctor(ctx context.Context) (core.RunOutput, error) {
	info := HistoryDoctorInfo{ArchivePath: historyArchivePath, Architecture: r.deps.Architecture}
	warnings := []v1alpha1.Diagnostic{}
	if r.deps.Processes == nil {
		warnings = append(warnings, historyDiagnostic("SSAR_NOT_FOUND", "ssar process support is unavailable"))
	} else if path, err := r.deps.Processes.LookPath("ssar"); err != nil {
		warnings = append(warnings, historyDiagnostic("SSAR_NOT_FOUND", "ssar is not installed or is not available on PATH"))
	} else {
		info.SSARPath = path
		version := r.deps.Processes.Run(ctx, core.ProcessSpec{Path: path, Args: []string{"--version"}, MaxStdout: 64 << 10, MaxStderr: 64 << 10})
		if version.Err == nil && version.ExitCode == 0 {
			info.SSARVersion = strings.TrimSpace(string(version.Stdout))
		} else {
			warnings = append(warnings, historyDiagnostic("SSAR_VERSION_UNAVAILABLE", "ssar was found but its version could not be read"))
		}
	}
	if _, err := r.deps.Files.Stat(historyArchivePath); err == nil {
		info.ArchivePresent = true
	} else {
		warnings = append(warnings, historyDiagnostic("SSAR_ARCHIVE_NOT_FOUND", "historical ssar data directory is not available"))
	}
	info.Ready = info.SSARPath != "" && info.ArchivePresent
	item, err := resultbuilder.NewItem("PerformanceHistoryDoctor", "ssar", "", info)
	if err != nil {
		return core.RunOutput{}, err
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: warnings, Errors: []v1alpha1.TargetError{}}, nil
}

func (r *Runner) historyCPU(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	requested, err := stringSliceOption(op, "cpu")
	if err != nil {
		return invalidOutput(err), nil
	}
	availableCounters, err := readCPUCounters(r.deps.Files)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "CPU list could not be read", err), nil
	}
	selected, err := selectNames(requested, sortedKeys(availableCounters), []string{"cpu"})
	if err != nil {
		return invalidOutput(err), nil
	}
	metrics := make([]string, 0, len(selected))
	for _, cpu := range selected {
		metrics = append(metrics, fmt.Sprintf("metric=d|cfile=stat|line_begin=%s|column=2-11|alias=%s_stat_{column}", cpu, cpu))
	}
	rows, output := r.runHistoryQuery(ctx, op, strings.Join(metrics, ";"))
	if output != nil {
		return *output, nil
	}
	items := []v1alpha1.Item{}
	for _, row := range rows {
		for _, cpu := range selected {
			stat, err := historyCPUStat(row, cpu)
			if err != nil {
				return failedOutput(v1alpha1.ErrorParseFailed, "ssar CPU output could not be parsed", err), nil
			}
			item, err := resultbuilder.NewItem("CPUStat", cpu, "", stat)
			if err != nil {
				return core.RunOutput{}, err
			}
			items = append(items, item)
		}
	}
	return historySuccessOutput(items, rows), nil
}

func (r *Runner) historyDiskIO(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	requested, err := stringSliceOption(op, "device")
	if err != nil {
		return invalidOutput(err), nil
	}
	availableCounters, err := readDiskCounters(r.deps.Files)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "block device list could not be read", err), nil
	}
	_, defaults, err := blockDeviceNames(r.deps.Files)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "block device list could not be read", err), nil
	}
	selected, err := selectNames(requested, sortedKeys(availableCounters), defaults)
	if err != nil {
		return invalidOutput(err), nil
	}
	metrics := []string{}
	for _, device := range selected {
		columns := []int{4, 6, 7, 8, 10, 11, 13, 14}
		names := []string{"rd_ios", "rd_sectors", "rd_ticks", "wr_ios", "wr_sectors", "wr_ticks", "ticks", "aveq"}
		for index, column := range columns {
			metrics = append(metrics, fmt.Sprintf("metric=d|cfile=diskstats|line_begin=%s|column=%d|alias=%s_disk_%s", device, column, device, names[index]))
		}
	}
	rows, output := r.runHistoryQuery(ctx, op, strings.Join(metrics, ";"))
	if output != nil {
		return *output, nil
	}
	items := []v1alpha1.Item{}
	for _, row := range rows {
		for _, device := range selected {
			stat, err := historyDiskStat(row, device)
			if err != nil {
				return failedOutput(v1alpha1.ErrorParseFailed, "ssar disk IO output could not be parsed", err), nil
			}
			item, err := resultbuilder.NewItem("DiskIOStat", device, "", stat)
			if err != nil {
				return core.RunOutput{}, err
			}
			items = append(items, item)
		}
	}
	return historySuccessOutput(items, rows), nil
}

func (r *Runner) historyNetwork(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	requested, err := stringSliceOption(op, "interface")
	if err != nil {
		return invalidOutput(err), nil
	}
	availableCounters, err := readNetworkCounters(r.deps.Files)
	if err != nil {
		return failedOutput(v1alpha1.ErrorExecutionFailed, "network interface list could not be read", err), nil
	}
	available := sortedKeys(availableCounters)
	defaults := []string{}
	for _, name := range available {
		if name != "lo" {
			defaults = append(defaults, name)
		}
	}
	selected, err := selectNames(requested, available, defaults)
	if err != nil {
		return invalidOutput(err), nil
	}
	metrics := []string{}
	for _, name := range selected {
		columns := []int{2, 3, 4, 5, 10, 11, 12, 13}
		aliases := []string{"rx_bytes", "rx_packets", "rx_errors", "rx_drops", "tx_bytes", "tx_packets", "tx_errors", "tx_drops"}
		for index, column := range columns {
			metrics = append(metrics, fmt.Sprintf("position=a|metric=d|cfile=dev|line_begin=%s:|column=%d|alias=%s_net_%s", name, column, name, aliases[index]))
		}
	}
	rows, output := r.runHistoryQuery(ctx, op, strings.Join(metrics, ";"))
	if output != nil {
		return *output, nil
	}
	items := []v1alpha1.Item{}
	for _, row := range rows {
		for _, name := range selected {
			stat, err := historyNetworkStat(row, name)
			if err != nil {
				return failedOutput(v1alpha1.ErrorParseFailed, "ssar network output could not be parsed", err), nil
			}
			item, err := resultbuilder.NewItem("NetworkStat", name, "", stat)
			if err != nil {
				return core.RunOutput{}, err
			}
			items = append(items, item)
		}
	}
	return historySuccessOutput(items, rows), nil
}

func (r *Runner) historyTCPRetrans(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	metrics := "metric=d|cfile=snmp|line=8|column=12|alias=tcp_out_segments;metric=d|cfile=snmp|line=8|column=13|alias=tcp_retrans_segments"
	rows, output := r.runHistoryQuery(ctx, op, metrics)
	if output != nil {
		return *output, nil
	}
	items := []v1alpha1.Item{}
	for _, row := range rows {
		out, err := historyFloat(row, "tcp_out_segments")
		if err != nil {
			return failedOutput(v1alpha1.ErrorParseFailed, "ssar TCP output could not be parsed", err), nil
		}
		retransmitted, err := historyFloat(row, "tcp_retrans_segments")
		if err != nil {
			return failedOutput(v1alpha1.ErrorParseFailed, "ssar TCP output could not be parsed", err), nil
		}
		stat := TCPRetransmissionStat{Timestamp: historyTimestamp(row), SegmentsOutPerSecond: out, RetransmittedPerSecond: retransmitted, RetransmissionPercent: safeRatio(retransmitted*100, out)}
		item, err := resultbuilder.NewItem("TCPRetransmissionStat", stat.Timestamp, "", stat)
		if err != nil {
			return core.RunOutput{}, err
		}
		items = append(items, item)
	}
	return historySuccessOutput(items, rows), nil
}

func (r *Runner) runHistoryQuery(ctx context.Context, op v1alpha1.Operation, expression string) ([]map[string]any, *core.RunOutput) {
	if r.deps.Processes == nil {
		output := failedOutput(v1alpha1.ErrorDependencyMissing, "ssar is required for performance history queries", fmt.Errorf("process executor is unavailable"))
		return nil, &output
	}
	path, err := r.deps.Processes.LookPath("ssar")
	if err != nil {
		output := failedOutput(v1alpha1.ErrorDependencyMissing, "ssar is required for performance history queries", err)
		return nil, &output
	}
	args := []string{"-P", "--api", "-o", expression}
	timeArgs, err := historyTimeArgs(op)
	if err != nil {
		output := invalidOutput(err)
		return nil, &output
	}
	args = append(args, timeArgs...)
	process := r.deps.Processes.Run(ctx, core.ProcessSpec{Path: path, Args: args, MaxStdout: maxHistoryOutput, MaxStderr: 1 << 20})
	if process.Err != nil || process.ExitCode != 0 {
		reason := strings.TrimSpace(string(process.Stderr))
		if reason == "" && process.Err != nil {
			reason = process.Err.Error()
		}
		output := failedOutput(v1alpha1.ErrorExecutionFailed, "ssar query failed", errors.New(reason))
		return nil, &output
	}
	rows, err := decodeHistoryRows(process.Stdout)
	if err != nil {
		output := failedOutput(v1alpha1.ErrorParseFailed, "ssar returned invalid JSON", err)
		return nil, &output
	}
	return rows, nil
}

func historyTimeArgs(op v1alpha1.Operation) ([]string, error) {
	from, err := historyStringOption(op, "from")
	if err != nil {
		return nil, err
	}
	to, err := historyStringOption(op, "to")
	if err != nil {
		return nil, err
	}
	interval, err := durationOption(op, "interval")
	if err != nil {
		return nil, err
	}
	if interval < time.Minute || interval%time.Minute != 0 {
		return nil, fmt.Errorf("--interval must be a positive whole number of minutes")
	}
	args := []string{}
	if from != "" {
		formatted, err := formatSSARTime(from)
		if err != nil {
			return nil, fmt.Errorf("invalid --from: %w", err)
		}
		args = append(args, "-b", formatted)
	} else {
		rangeDuration, err := durationOption(op, "range")
		if err != nil {
			return nil, err
		}
		if rangeDuration < time.Minute || rangeDuration%time.Minute != 0 {
			return nil, fmt.Errorf("--range must be a positive whole number of minutes")
		}
		args = append(args, "-r", strconv.FormatInt(int64(rangeDuration/time.Minute), 10))
	}
	if to != "" {
		formatted, err := formatSSARTime(to)
		if err != nil {
			return nil, fmt.Errorf("invalid --to: %w", err)
		}
		args = append(args, "-f", formatted)
	}
	return append(args, "-i", strconv.FormatInt(int64(interval/time.Minute), 10)), nil
}

func formatSSARTime(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", fmt.Errorf("expected RFC3339 timestamp")
	}
	return parsed.In(time.Local).Format("2006-01-02T15:04:05"), nil
}

func historyCPUStat(row map[string]any, cpu string) (CPUStat, error) {
	values := make([]float64, 10)
	for index := range values {
		value, err := historyFloat(row, fmt.Sprintf("%s_stat_%d", cpu, index+2))
		if err != nil {
			return CPUStat{}, err
		}
		values[index] = value
	}
	total := values[0] + values[1] + values[2] + values[3] + values[4] + values[5] + values[6] + values[7]
	return CPUStat{
		Timestamp: historyTimestamp(row), CPU: cpu, UserPercent: safeRatio(values[0]*100, total),
		SystemPercent: safeRatio(values[2]*100, total), IOWaitPercent: safeRatio(values[4]*100, total),
		IRQPercent: safeRatio(values[5]*100, total), SoftIRQPercent: safeRatio(values[6]*100, total),
		IdlePercent: safeRatio(values[3]*100, total), StealPercent: safeRatio(values[7]*100, total),
		GuestPercent: safeRatio(values[8]*100, total), UtilPercent: safeRatio((total-values[3]-values[4]-values[7])*100, total),
	}, nil
}

func historyDiskStat(row map[string]any, device string) (DiskIOStat, error) {
	prefix := device + "_disk_"
	readIOs, err := historyFloat(row, prefix+"rd_ios")
	if err != nil {
		return DiskIOStat{}, err
	}
	writeIOs, err := historyFloat(row, prefix+"wr_ios")
	if err != nil {
		return DiskIOStat{}, err
	}
	readSectors, err := historyFloat(row, prefix+"rd_sectors")
	if err != nil {
		return DiskIOStat{}, err
	}
	writeSectors, err := historyFloat(row, prefix+"wr_sectors")
	if err != nil {
		return DiskIOStat{}, err
	}
	readMillis, err := historyFloat(row, prefix+"rd_ticks")
	if err != nil {
		return DiskIOStat{}, err
	}
	writeMillis, err := historyFloat(row, prefix+"wr_ticks")
	if err != nil {
		return DiskIOStat{}, err
	}
	ioMillis, err := historyFloat(row, prefix+"ticks")
	if err != nil {
		return DiskIOStat{}, err
	}
	weightedMillis, err := historyFloat(row, prefix+"aveq")
	if err != nil {
		return DiskIOStat{}, err
	}
	totalIOs := readIOs + writeIOs
	return DiskIOStat{
		Timestamp: historyTimestamp(row), Device: device, ReadsPerSecond: readIOs, WritesPerSecond: writeIOs,
		ReadBytesPerSecond: readSectors * 512, WriteBytesPerSecond: writeSectors * 512,
		AverageRequestBytes: safeRatio((readSectors+writeSectors)*512, totalIOs), AverageWaitMillis: safeRatio(readMillis+writeMillis, totalIOs),
		ReadWaitMillis: safeRatio(readMillis, readIOs), WriteWaitMillis: safeRatio(writeMillis, writeIOs),
		AverageQueueDepth: weightedMillis / 1000, UtilizationPercent: min(ioMillis/10, 100),
	}, nil
}

func historyNetworkStat(row map[string]any, name string) (NetworkStat, error) {
	values := map[string]float64{}
	for _, field := range []string{"rx_bytes", "rx_packets", "rx_errors", "rx_drops", "tx_bytes", "tx_packets", "tx_errors", "tx_drops"} {
		value, err := historyFloat(row, name+"_net_"+field)
		if err != nil {
			return NetworkStat{}, err
		}
		values[field] = value
	}
	return NetworkStat{
		Timestamp: historyTimestamp(row), Interface: name, ReceiveBytesPerSec: values["rx_bytes"], TransmitBytesPerSec: values["tx_bytes"],
		ReceivePacketsPerSec: values["rx_packets"], TransmitPacketsPerSec: values["tx_packets"],
		ErrorsPerSecond: values["rx_errors"] + values["tx_errors"], DropsPerSecond: values["rx_drops"] + values["tx_drops"],
	}, nil
}

func decodeHistoryRows(data []byte) ([]map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var rows []map[string]any
	if err := decoder.Decode(&rows); err == nil {
		return rows, nil
	}
	rows = []map[string]any{}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	for {
		var row map[string]any
		err := decoder.Decode(&row)
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
}

func historyFloat(row map[string]any, key string) (float64, error) {
	value, ok := row[key]
	if !ok {
		return 0, fmt.Errorf("field %s is missing", key)
	}
	switch number := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(number.String(), 64)
		return parsed, err
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
		return parsed, err
	case float64:
		return number, nil
	default:
		return 0, fmt.Errorf("field %s has unsupported type %T", key, value)
	}
}

func historyTimestamp(row map[string]any) string {
	value, _ := row["collect_datetime"].(string)
	parsed, err := time.ParseInLocation("2006-01-02T15:04:05", value, time.Local)
	if err != nil {
		return value
	}
	return parsed.Format(time.RFC3339)
}

func historyStringOption(op v1alpha1.Operation, name string) (string, error) {
	raw, ok := op.Options[name]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("--%s must be a string", name)
	}
	return strings.TrimSpace(value), nil
}

func historySuccessOutput(items []v1alpha1.Item, rows []map[string]any) core.RunOutput {
	warnings := []v1alpha1.Diagnostic{}
	if len(rows) == 0 {
		warnings = append(warnings, historyDiagnostic("NO_HISTORICAL_DATA", "ssar returned no data for the selected time range"))
	}
	return core.RunOutput{Items: items, Warnings: warnings, Errors: []v1alpha1.TargetError{}}
}

func historyDiagnostic(code, message string) v1alpha1.Diagnostic {
	return v1alpha1.Diagnostic{Code: code, Message: message, Details: map[string]string{}}
}
