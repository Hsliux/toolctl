package batch

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

type ContainerResource struct {
	Container        string  `json:"container"`
	Runtime          string  `json:"runtime"`
	PID              int     `json:"pid"`
	CgroupPath       string  `json:"cgroupPath"`
	CPUUsageSeconds  float64 `json:"cpuUsageSeconds"`
	MemoryBytes      uint64  `json:"memoryBytes"`
	MemoryLimitBytes uint64  `json:"memoryLimitBytes,omitempty"`
	PIDs             uint64  `json:"pids"`
	ReadBytes        uint64  `json:"readBytes"`
	WriteBytes       uint64  `json:"writeBytes"`
	ReadIOs          uint64  `json:"readIOs"`
	WriteIOs         uint64  `json:"writeIOs"`
}

func (r *Runner) containerStats(_ context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	mode := strings.ToLower(strings.TrimSpace(op.Name))
	if mode == "" {
		mode = "all"
	}
	if mode != "all" && mode != "cpu" && mode != "mem" && mode != "io" {
		return invalidFailure(fmt.Errorf("container resource must be cpu, mem, io, or omitted")), nil
	}
	selectors, err := stringsOption(op, "container")
	if err != nil {
		return invalidFailure(err), nil
	}
	runtimes, err := stringsOption(op, "runtime")
	if err != nil {
		return invalidFailure(err), nil
	}
	runtimeSet := map[string]bool{}
	for _, value := range runtimes {
		runtimeSet[strings.ToLower(strings.TrimSpace(value))] = true
	}
	containers, err := discoverContainers(r.deps.Files)
	if err != nil {
		return core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{{Code: v1alpha1.ErrorExecutionFailed, Message: "container processes could not be discovered", Details: map[string]string{"reason": err.Error()}}}}, nil
	}
	containers, err = selectContainers(containers, selectors, runtimeSet)
	if err != nil {
		return invalidFailure(err), nil
	}
	resources := []ContainerResource{}
	warnings := []v1alpha1.Diagnostic{}
	for _, container := range containers {
		resource, err := readContainerResource(r.deps.Files, container)
		if err != nil {
			warnings = append(warnings, v1alpha1.Diagnostic{Code: "CGROUP_STATS_UNAVAILABLE", Message: "container cgroup v2 statistics are unavailable", Details: map[string]string{"container": container.ID, "reason": err.Error()}})
			continue
		}
		resources = append(resources, resource)
	}
	sort.Slice(resources, func(i, j int) bool {
		switch mode {
		case "cpu":
			return resources[i].CPUUsageSeconds > resources[j].CPUUsageSeconds
		case "mem":
			return resources[i].MemoryBytes > resources[j].MemoryBytes
		case "io":
			return resources[i].ReadBytes+resources[i].WriteBytes > resources[j].ReadBytes+resources[j].WriteBytes
		default:
			return resources[i].Container < resources[j].Container
		}
	})
	output := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: warnings, Errors: []v1alpha1.TargetError{}}
	for _, resource := range resources {
		item, err := resultbuilder.NewItem("ContainerResource", resource.Container, resource.Container, resource)
		if err != nil {
			return core.RunOutput{}, err
		}
		output.Items = append(output.Items, item)
	}
	return output, nil
}

func readContainerResource(files core.FileSystem, container containerProcess) (ContainerResource, error) {
	clean := path.Clean("/" + strings.TrimPrefix(container.CgroupPath, "/"))
	if strings.Contains(clean, "..") || clean == "/" {
		return ContainerResource{}, fmt.Errorf("invalid cgroup path %q", container.CgroupPath)
	}
	base := "/sys/fs/cgroup" + clean
	resource := ContainerResource{Container: container.ID, Runtime: container.Runtime, PID: container.PID, CgroupPath: clean}
	cpu, err := files.ReadFile(base+"/cpu.stat", 1<<20)
	if err != nil {
		return resource, err
	}
	for _, line := range strings.Split(string(cpu), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			value, _ := strconv.ParseUint(fields[1], 10, 64)
			resource.CPUUsageSeconds = float64(value) / 1e6
		}
	}
	resource.MemoryBytes, _ = readUintFile(files, base+"/memory.current")
	if limit, err := readTextFile(files, base+"/memory.max"); err == nil && limit != "max" {
		resource.MemoryLimitBytes, _ = strconv.ParseUint(limit, 10, 64)
	}
	resource.PIDs, _ = readUintFile(files, base+"/pids.current")
	if data, err := files.ReadFile(base+"/io.stat", 4<<20); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			for _, field := range strings.Fields(line)[minIntLocal(1, len(strings.Fields(line))):] {
				key, value, ok := strings.Cut(field, "=")
				if !ok {
					continue
				}
				number, _ := strconv.ParseUint(value, 10, 64)
				switch key {
				case "rbytes":
					resource.ReadBytes += number
				case "wbytes":
					resource.WriteBytes += number
				case "rios":
					resource.ReadIOs += number
				case "wios":
					resource.WriteIOs += number
				}
			}
		}
	}
	return resource, nil
}

func readTextFile(files core.FileSystem, name string) (string, error) {
	data, err := files.ReadFile(name, 1<<20)
	return strings.TrimSpace(string(data)), err
}
func readUintFile(files core.FileSystem, name string) (uint64, error) {
	text, err := readTextFile(files, name)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(text, 10, 64)
}
func minIntLocal(a, b int) int {
	if a < b {
		return a
	}
	return b
}
