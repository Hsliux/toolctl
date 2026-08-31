package bench

import (
	"context"
	"encoding/json"
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

func (r *Runner) runDevice(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	if r.deps.OperatingSystem != "" && r.deps.OperatingSystem != "linux" {
		return failure(v1alpha1.ErrorUnsupportedPlatform, "raw device benchmark requires Linux", nil), nil
	}
	options, profile, err := parseDeviceOptions(op)
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	device, deviceBytes, err := validateRawDevice(r.deps.Files, op.Name)
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, "raw benchmark target is not an unused whole block device", err), nil
	}
	if options.SizeBytes > deviceBytes {
		return failure(v1alpha1.ErrorInvalidArgument, "benchmark size exceeds raw device capacity", fmt.Errorf("device=%d requested=%d", deviceBytes, options.SizeBytes)), nil
	}
	destructive := profile.RW != "read" && profile.RW != "randread"
	if destructive && !options.DryRun && !options.DestroyData {
		return failure(v1alpha1.ErrorInvalidArgument, "write workload destroys data in the tested region; verify the device identity and add --destroy-data", nil), nil
	}
	if op.TimeoutMS > 0 && options.Duration+options.Warmup+time.Second >= time.Duration(op.TimeoutMS)*time.Millisecond {
		return failure(v1alpha1.ErrorInvalidArgument, "operation timeout is too short for raw device benchmark; increase --timeout", nil), nil
	}
	fioPath, err := r.deps.Processes.LookPath("fio")
	if err != nil {
		return failure(v1alpha1.ErrorDependencyMissing, "fio is required; run toolctl bench disk doctor", err), nil
	}
	args := rawDeviceArgs(device, options, profile)
	if options.DryRun {
		plan := DeviceBenchmarkPlan{Mode: "dry-run", Profile: options.Profile, Target: device, DeviceBytes: deviceBytes, SizeBytes: options.SizeBytes, Destructive: destructive, Confirmed: options.DestroyData, FIOPath: fioPath, Arguments: args}
		item, itemErr := resultbuilder.NewItem("DeviceBenchmarkPlan", device, "", plan)
		if itemErr != nil {
			return core.RunOutput{}, itemErr
		}
		return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
	}
	process := r.deps.Processes.Run(ctx, core.ProcessSpec{Path: fioPath, Args: args, MaxStdout: maxFIOOutput, MaxStderr: maxFIOError})
	if process.TimedOut {
		return failure(v1alpha1.ErrorTimeout, "fio raw device benchmark timed out", processReason(process)), nil
	}
	if process.Err != nil || process.ExitCode != 0 {
		return failure(v1alpha1.ErrorExecutionFailed, "fio raw device benchmark failed", processReason(process)), nil
	}
	parsed, err := parseFIOJSON(process.Stdout)
	if err != nil {
		return failure(v1alpha1.ErrorParseFailed, "fio JSON output could not be parsed", err), nil
	}
	parsed.Mode, parsed.Profile, parsed.Target, parsed.File, parsed.FileKept = "device", options.Profile, device, device, true
	parsed.Destructive = destructive
	parsed.SizeBytes, parsed.DurationMS, parsed.WarmupMS = options.SizeBytes, options.Duration.Milliseconds(), options.Warmup.Milliseconds()
	parsed.Jobs, parsed.QueueDepth, parsed.BlockSize = options.Jobs, options.Depth, options.BlockSize
	item, err := resultbuilder.NewItem("DeviceBenchmarkResult", device, "", parsed)
	if err != nil {
		return core.RunOutput{}, err
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
}

func parseDeviceOptions(op v1alpha1.Operation) (diskOptions, profileSpec, error) {
	copyOperation := op
	copyOperation.Options = make(map[string]json.RawMessage, len(op.Options)+2)
	for name, value := range op.Options {
		copyOperation.Options[name] = value
	}
	copyOperation.Options["keep-file"] = json.RawMessage("true")
	copyOperation.Options["force"] = json.RawMessage("false")
	options, profile, err := parseDiskOptions(copyOperation)
	if err != nil {
		return diskOptions{}, profileSpec{}, err
	}
	if raw, ok := op.Options["destroy-data"]; ok {
		if err := json.Unmarshal(raw, &options.DestroyData); err != nil {
			return diskOptions{}, profileSpec{}, fmt.Errorf("invalid --destroy-data: %w", err)
		}
	}
	return options, profile, nil
}

func validateRawDevice(files core.FileSystem, value string) (string, uint64, error) {
	if strings.TrimSpace(value) == "" {
		return "", 0, fmt.Errorf("device path is empty")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", 0, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", 0, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", 0, err
	}
	if info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0 {
		return "", 0, fmt.Errorf("%s is not a block device", resolved)
	}
	name := filepath.Base(resolved)
	base := "/sys/class/block/" + name
	if _, err := files.Stat(base + "/partition"); err == nil {
		return "", 0, fmt.Errorf("partitions are not accepted; select an unused whole device")
	}
	if entries, err := files.ReadDir(base); err == nil {
		for _, entry := range entries {
			if _, err := files.Stat(base + "/" + entry.Name() + "/partition"); err == nil {
				return "", 0, fmt.Errorf("device contains partition %s", entry.Name())
			}
		}
	}
	if entries, err := files.ReadDir(base + "/holders"); err == nil && len(entries) > 0 {
		return "", 0, fmt.Errorf("device is held by %s", entries[0].Name())
	}
	for _, pseudoFile := range []string{"/proc/self/mountinfo", "/proc/swaps", "/proc/mdstat"} {
		if data, err := files.ReadFile(pseudoFile, 4<<20); err == nil && deviceReferenced(string(data), name, resolved) {
			return "", 0, fmt.Errorf("device or one of its partitions is referenced by %s", pseudoFile)
		}
	}
	sizeText, err := readDeviceValue(files, base+"/size")
	if err != nil {
		return "", 0, fmt.Errorf("read device capacity: %w", err)
	}
	sectors, err := strconv.ParseUint(sizeText, 10, 64)
	if err != nil || sectors == 0 || sectors > ^uint64(0)/512 {
		return "", 0, fmt.Errorf("invalid device capacity %q", sizeText)
	}
	return resolved, sectors * 512, nil
}

func deviceReferenced(content, name, resolved string) bool {
	if strings.Contains(content, resolved) || strings.Contains(content, "/dev/"+name) {
		return true
	}
	for _, prefix := range []string{"/dev/" + name + "p", "/dev/" + name} {
		for digit := '0'; digit <= '9'; digit++ {
			if strings.Contains(content, prefix+string(digit)) {
				return true
			}
		}
	}
	return false
}

func readDeviceValue(files core.FileSystem, path string) (string, error) {
	data, err := files.ReadFile(path, 64<<10)
	return strings.TrimSpace(string(data)), err
}

func rawDeviceArgs(device string, options diskOptions, profile profileSpec) []string {
	args := benchmarkArgs(device, options, profile)
	args[0] = "--name=toolctl-device-" + options.Profile
	return args
}
