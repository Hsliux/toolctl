package bench

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
)

type diskOptions struct {
	Profile, BlockSize string
	SizeBytes          uint64
	Duration, Warmup   time.Duration
	Jobs, Depth        int
	KeepFile           bool
	DryRun, Force      bool
	DestroyData        bool
}

type profileSpec struct {
	RW, BlockSize string
	ReadMix       int
	NeedsPrepare  bool
}

var profiles = map[string]profileSpec{
	"seq-read":   {RW: "read", BlockSize: "1M", NeedsPrepare: true},
	"seq-write":  {RW: "write", BlockSize: "1M"},
	"rand-read":  {RW: "randread", BlockSize: "4K", NeedsPrepare: true},
	"rand-write": {RW: "randwrite", BlockSize: "4K"},
	"rand-rw":    {RW: "randrw", BlockSize: "4K", ReadMix: 70, NeedsPrepare: true},
}

func parseDiskOptions(op v1alpha1.Operation) (diskOptions, profileSpec, error) {
	stringValue := func(name string) (string, error) {
		var value string
		if err := json.Unmarshal(op.Options[name], &value); err != nil {
			return "", fmt.Errorf("invalid --%s: %w", name, err)
		}
		return value, nil
	}
	intValue := func(name string) (int, error) {
		var value int64
		if err := json.Unmarshal(op.Options[name], &value); err != nil {
			return 0, fmt.Errorf("invalid --%s: %w", name, err)
		}
		return int(value), nil
	}
	boolValue := func(name string) (bool, error) {
		var value bool
		if err := json.Unmarshal(op.Options[name], &value); err != nil {
			return false, fmt.Errorf("invalid --%s: %w", name, err)
		}
		return value, nil
	}
	profile, err := stringValue("profile")
	if err != nil {
		return diskOptions{}, profileSpec{}, err
	}
	profile = strings.ToLower(strings.TrimSpace(profile))
	spec, ok := profiles[profile]
	if !ok {
		return diskOptions{}, profileSpec{}, fmt.Errorf("unsupported profile %q", profile)
	}
	sizeText, err := stringValue("size")
	if err != nil {
		return diskOptions{}, profileSpec{}, err
	}
	size, err := parseSize(sizeText)
	if err != nil {
		return diskOptions{}, profileSpec{}, fmt.Errorf("invalid --size: %w", err)
	}
	durationMS, err := intValue("duration")
	if err != nil {
		return diskOptions{}, profileSpec{}, err
	}
	warmupMS, err := intValue("warmup")
	if err != nil {
		return diskOptions{}, profileSpec{}, err
	}
	jobs, err := intValue("jobs")
	if err != nil {
		return diskOptions{}, profileSpec{}, err
	}
	depth, err := intValue("depth")
	if err != nil {
		return diskOptions{}, profileSpec{}, err
	}
	blockSize, err := stringValue("block-size")
	if err != nil {
		return diskOptions{}, profileSpec{}, err
	}
	keepFile, err := boolValue("keep-file")
	if err != nil {
		return diskOptions{}, profileSpec{}, err
	}
	dryRun, err := boolValue("dry-run")
	if err != nil {
		return diskOptions{}, profileSpec{}, err
	}
	force, err := boolValue("force")
	if err != nil {
		return diskOptions{}, profileSpec{}, err
	}
	if blockSize == "auto" {
		blockSize = spec.BlockSize
	}
	blockBytes, err := parseSize(blockSize)
	if err != nil {
		return diskOptions{}, profileSpec{}, fmt.Errorf("invalid --block-size: %w", err)
	}
	if blockBytes < 512 || blockBytes > 16<<20 {
		return diskOptions{}, profileSpec{}, fmt.Errorf("block size must be between 512B and 16M")
	}
	options := diskOptions{Profile: profile, BlockSize: strings.ToUpper(blockSize), SizeBytes: size, Duration: time.Duration(durationMS) * time.Millisecond, Warmup: time.Duration(warmupMS) * time.Millisecond, Jobs: jobs, Depth: depth, KeepFile: keepFile, DryRun: dryRun, Force: force}
	if size < 64<<20 || size > 1<<40 {
		return diskOptions{}, profileSpec{}, fmt.Errorf("size must be between 64M and 1T")
	}
	if options.Duration < time.Second || options.Duration > 24*time.Hour {
		return diskOptions{}, profileSpec{}, fmt.Errorf("duration must be between 1s and 24h")
	}
	if options.Warmup < 0 || options.Warmup > 10*time.Minute {
		return diskOptions{}, profileSpec{}, fmt.Errorf("warmup must be between 0 and 10m")
	}
	if jobs < 1 || jobs > 64 {
		return diskOptions{}, profileSpec{}, fmt.Errorf("jobs must be between 1 and 64")
	}
	if depth < 1 || depth > 1024 {
		return diskOptions{}, profileSpec{}, fmt.Errorf("depth must be between 1 and 1024")
	}
	if jobs*depth > 4096 {
		return diskOptions{}, profileSpec{}, fmt.Errorf("jobs multiplied by depth cannot exceed 4096")
	}
	return options, spec, nil
}

func parseSize(value string) (uint64, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	if value == "" {
		return 0, fmt.Errorf("size is empty")
	}
	unit := ""
	for len(value) > 0 && (value[len(value)-1] < '0' || value[len(value)-1] > '9') {
		unit = string(value[len(value)-1]) + unit
		value = value[:len(value)-1]
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("invalid number")
	}
	multipliers := map[string]float64{"": 1, "B": 1, "K": 1 << 10, "KB": 1 << 10, "KIB": 1 << 10, "M": 1 << 20, "MB": 1 << 20, "MIB": 1 << 20, "G": 1 << 30, "GB": 1 << 30, "GIB": 1 << 30, "T": 1 << 40, "TB": 1 << 40, "TIB": 1 << 40}
	multiplier, ok := multipliers[unit]
	if !ok {
		return 0, fmt.Errorf("unsupported unit %q", unit)
	}
	result := number * multiplier
	if result > float64(^uint64(0)) {
		return 0, fmt.Errorf("size is too large")
	}
	return uint64(result), nil
}
