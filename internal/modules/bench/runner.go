package bench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

func (r *Runner) ExecuteStream(ctx context.Context, op v1alpha1.Operation, emit func(core.RunOutput) error) error {
	if op.Capability != capabilityNetRun {
		return fmt.Errorf("benchmark capability %s does not support live streaming", op.Capability)
	}
	output, err := r.runNetworkClientWithProgress(ctx, op, func(result NetworkBenchmarkResult) error {
		snapshot, buildErr := networkResult(result)
		if buildErr != nil {
			return buildErr
		}
		return emit(snapshot)
	})
	if err != nil {
		return err
	}
	return emit(output)
}

var _ core.StreamingRunner = (*Runner)(nil)

const (
	maxFIOOutput = 16 << 20
	maxFIOError  = 1 << 20
)

func (r *Runner) Execute(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	switch op.Capability {
	case capabilityBurnRun:
		return r.runBurn(ctx, op)
	case capabilityDiskDoctor:
		return r.doctor(ctx)
	case capabilityDiskRun:
		return r.runDisk(ctx, op)
	case capabilityCPURun:
		return r.runCPU(ctx, op)
	case capabilityMemoryRun:
		return r.runMemory(ctx, op)
	case capabilityDeviceRun:
		return r.runDevice(ctx, op)
	case capabilityNetRun:
		return r.runNetworkClient(ctx, op)
	case capabilityNetLatency:
		return r.runNetworkLatency(ctx, op)
	case capabilityNetServe:
		return r.runNetworkServer(ctx, op)
	default:
		return core.RunOutput{}, fmt.Errorf("benchmark module does not support %s", op.Capability)
	}
}

func (r *Runner) doctor(ctx context.Context) (core.RunOutput, error) {
	info := DoctorInfo{Message: "fio is not installed or not available in PATH"}
	path, err := r.deps.Processes.LookPath("fio")
	if err == nil {
		result := r.deps.Processes.Run(ctx, core.ProcessSpec{Path: path, Args: []string{"--version"}, MaxStdout: 64 << 10, MaxStderr: 64 << 10})
		info.FIOPath = path
		version := strings.TrimSpace(string(result.Stdout))
		if result.Err == nil && result.ExitCode == 0 && strings.HasPrefix(version, "fio-") {
			info.Ready, info.FIOVersion, info.Message = true, version, "fio is ready"
		} else {
			info.Message = "fio was found but its version command failed"
		}
	}
	item, err := resultbuilder.NewItem("DiskBenchmarkDoctor", "fio", "", info)
	if err != nil {
		return core.RunOutput{}, err
	}
	warnings := []v1alpha1.Diagnostic{}
	if !info.Ready {
		warnings = append(warnings, v1alpha1.Diagnostic{Code: "FIO_UNAVAILABLE", Message: info.Message, Details: map[string]string{"suggestion": "install the fio package and rerun toolctl bench disk doctor"}})
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: warnings, Errors: []v1alpha1.TargetError{}}, nil
}

func (r *Runner) runDisk(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	if r.deps.OperatingSystem != "" && r.deps.OperatingSystem != "linux" {
		return failure(v1alpha1.ErrorUnsupportedPlatform, "disk benchmark requires Linux", nil), nil
	}
	options, profile, err := parseDiskOptions(op)
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	if op.TimeoutMS > 0 && options.Duration+options.Warmup+time.Second >= time.Duration(op.TimeoutMS)*time.Millisecond {
		return failure(v1alpha1.ErrorInvalidArgument, "operation timeout is too short for duration and warmup; increase --timeout", nil), nil
	}
	directory, err := resolveDirectory(op.Name)
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, "benchmark target must be an existing writable directory", err), nil
	}
	stats, err := r.deps.Files.StatFS(directory)
	if err != nil {
		return failure(v1alpha1.ErrorExecutionFailed, "target filesystem statistics could not be read", err), nil
	}
	headroom := stats.AvailableBytes / 10
	if headroom < 256<<20 {
		headroom = 256 << 20
	}
	if stats.AvailableBytes <= headroom || options.SizeBytes > stats.AvailableBytes-headroom {
		return failure(v1alpha1.ErrorInvalidArgument, "insufficient free space for benchmark file and safety headroom", fmt.Errorf("available=%d requested=%d headroom=%d", stats.AvailableBytes, options.SizeBytes, headroom)), nil
	}
	fioPath, err := r.deps.Processes.LookPath("fio")
	if err != nil {
		return failure(v1alpha1.ErrorDependencyMissing, "fio is required; run toolctl bench disk doctor", err), nil
	}
	file := filepath.Join(directory, ".toolctl-bench-"+safeID(op.ID)+".fio")
	mountpoint := benchmarkMountpoint(r.deps.Files, directory)
	rootFilesystem := mountpoint == "/"
	if rootFilesystem && !options.DryRun && !options.Force {
		return failure(v1alpha1.ErrorInvalidArgument, "write benchmark target is on the root filesystem; inspect with --dry-run and rerun with --force", nil), nil
	}
	if options.DryRun {
		plan := DiskBenchmarkPlan{
			Mode: "dry-run", Profile: options.Profile, Target: directory, Mountpoint: mountpoint, RootFilesystem: rootFilesystem,
			File: file, SizeBytes: options.SizeBytes, DurationMS: options.Duration.Milliseconds(), WarmupMS: options.Warmup.Milliseconds(),
			Jobs: options.Jobs, QueueDepth: options.Depth, BlockSize: options.BlockSize, FIOPath: fioPath, NeedsPreparation: profile.NeedsPrepare,
			Arguments: benchmarkArgs(file, options, profile), PrepareArguments: []string{},
		}
		if profile.NeedsPrepare {
			plan.PrepareArguments = prepareArgs(file, options.SizeBytes)
		}
		item, itemErr := resultbuilder.NewItem("DiskBenchmarkPlan", directory, "", plan)
		if itemErr != nil {
			return core.RunOutput{}, itemErr
		}
		return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
	}
	handle, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return failure(v1alpha1.ErrorExecutionFailed, "temporary benchmark file could not be created safely", err), nil
	}
	if err := handle.Close(); err != nil {
		_ = os.Remove(file)
		return failure(v1alpha1.ErrorExecutionFailed, "temporary benchmark file could not be closed", err), nil
	}
	warnings := []v1alpha1.Diagnostic{}
	cleanup := func() {
		if options.KeepFile {
			return
		}
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			warnings = append(warnings, v1alpha1.Diagnostic{Code: "BENCHMARK_FILE_CLEANUP_FAILED", Message: "temporary benchmark file could not be removed", Details: map[string]string{"path": file, "reason": err.Error()}})
		}
	}
	if profile.NeedsPrepare {
		prepare := r.deps.Processes.Run(ctx, core.ProcessSpec{Path: fioPath, Args: prepareArgs(file, options.SizeBytes), MaxStdout: maxFIOOutput, MaxStderr: maxFIOError})
		if prepare.Err != nil || prepare.ExitCode != 0 {
			cleanup()
			return withWarnings(failure(v1alpha1.ErrorExecutionFailed, "fio preparation pass failed", processReason(prepare)), warnings), nil
		}
	}
	process := r.deps.Processes.Run(ctx, core.ProcessSpec{Path: fioPath, Args: benchmarkArgs(file, options, profile), MaxStdout: maxFIOOutput, MaxStderr: maxFIOError})
	if process.TimedOut {
		cleanup()
		return withWarnings(failure(v1alpha1.ErrorTimeout, "fio benchmark timed out", processReason(process)), warnings), nil
	}
	if process.Err != nil || process.ExitCode != 0 {
		cleanup()
		return withWarnings(failure(v1alpha1.ErrorExecutionFailed, "fio benchmark failed", processReason(process)), warnings), nil
	}
	parsed, err := parseFIOJSON(process.Stdout)
	if err != nil {
		cleanup()
		return withWarnings(failure(v1alpha1.ErrorParseFailed, "fio JSON output could not be parsed", err), warnings), nil
	}
	parsed.Mode, parsed.Profile, parsed.Target, parsed.Mountpoint, parsed.RootFilesystem, parsed.File, parsed.FileKept = "run", options.Profile, directory, mountpoint, rootFilesystem, file, options.KeepFile
	parsed.SizeBytes, parsed.DurationMS, parsed.WarmupMS = options.SizeBytes, options.Duration.Milliseconds(), options.Warmup.Milliseconds()
	parsed.Jobs, parsed.QueueDepth, parsed.BlockSize = options.Jobs, options.Depth, options.BlockSize
	cleanup()
	item, err := resultbuilder.NewItem("DiskBenchmarkResult", directory, "", parsed)
	if err != nil {
		return core.RunOutput{}, err
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: warnings, Errors: []v1alpha1.TargetError{}}, nil
}

func benchmarkMountpoint(files core.FileSystem, target string) string {
	data, err := files.ReadFile("/proc/self/mountinfo", 4<<20)
	if err != nil {
		return ""
	}
	best := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mountpoint := unescapeMountinfo(fields[4])
		matches := target == mountpoint
		if mountpoint == "/" {
			matches = strings.HasPrefix(target, "/")
		} else if strings.HasPrefix(target, strings.TrimSuffix(mountpoint, "/")+"/") {
			matches = true
		}
		if !matches {
			continue
		}
		if len(mountpoint) > len(best) {
			best = mountpoint
		}
	}
	return best
}

func unescapeMountinfo(value string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(value)
}

func resolveDirectory(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("target directory is empty")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	return resolved, nil
}

func safeID(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' {
			builder.WriteRune(character)
		}
	}
	if builder.Len() == 0 {
		return strconv.Itoa(os.Getpid())
	}
	if builder.Len() > 64 {
		return builder.String()[:64]
	}
	return builder.String()
}

func prepareArgs(file string, size uint64) []string {
	return []string{"--name=toolctl-prepare", "--filename=" + file, "--rw=write", "--bs=1M", "--direct=1", "--ioengine=libaio", "--iodepth=16", "--numjobs=1", "--size=" + strconv.FormatUint(size, 10), "--end_fsync=1", "--output-format=json", "--eta=never"}
}

func benchmarkArgs(file string, options diskOptions, profile profileSpec) []string {
	args := []string{"--name=toolctl-" + options.Profile, "--filename=" + file, "--rw=" + profile.RW, "--bs=" + options.BlockSize, "--direct=1", "--ioengine=libaio", "--iodepth=" + strconv.Itoa(options.Depth), "--numjobs=" + strconv.Itoa(options.Jobs), "--size=" + strconv.FormatUint(options.SizeBytes, 10), "--time_based", "--runtime=" + strconv.FormatInt(options.Duration.Milliseconds(), 10) + "ms", "--ramp_time=" + strconv.FormatInt(options.Warmup.Milliseconds(), 10) + "ms", "--group_reporting=1", "--randrepeat=0", "--invalidate=1", "--output-format=json", "--eta=never"}
	if profile.ReadMix > 0 {
		args = append(args, "--rwmixread="+strconv.Itoa(profile.ReadMix))
	}
	return args
}

func processReason(result core.ProcessResult) error {
	message := strings.TrimSpace(string(result.Stderr))
	if message == "" {
		message = strings.TrimSpace(string(result.Stdout))
	}
	if message == "" && result.Err != nil {
		message = result.Err.Error()
	}
	if message == "" {
		message = fmt.Sprintf("exit code %d", result.ExitCode)
	}
	if len(message) > 4096 {
		message = message[:4093] + "..."
	}
	return fmt.Errorf("%s", message)
}

func failure(code v1alpha1.ErrorCode, message string, err error) core.RunOutput {
	details := map[string]string{}
	if err != nil {
		details["reason"] = err.Error()
	}
	return core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{{Code: code, Message: message, Details: details}}}
}

func withWarnings(output core.RunOutput, warnings []v1alpha1.Diagnostic) core.RunOutput {
	output.Warnings = warnings
	return output
}
