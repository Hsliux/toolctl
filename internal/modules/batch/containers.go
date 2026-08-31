package batch

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

type containerProcess struct {
	ID, Runtime, CgroupPath string
	PID                     int
}

var containerPatterns = []struct {
	runtime string
	pattern *regexp.Regexp
}{
	{"docker", regexp.MustCompile(`(?:^|/)(?:docker[-/])([0-9a-f]{12,64})(?:\.scope)?(?:/|$)`)},
	{"containerd", regexp.MustCompile(`(?:^|/)cri-containerd-([0-9a-f]{12,64})\.scope(?:/|$)`)},
	{"cri-o", regexp.MustCompile(`(?:^|/)crio-([0-9a-f]{12,64})\.scope(?:/|$)`)},
	{"podman", regexp.MustCompile(`(?:^|/)libpod-([0-9a-f]{12,64})\.scope(?:/|$)`)},
	{"container", regexp.MustCompile(`(?:^|/)([0-9a-f]{64})(?:/|$)`)},
}

func (r *Runner) executeContainers(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	concurrency, err := intOption(op, "jobs")
	if err != nil {
		return invalidFailure(err), nil
	}
	timeout, err := durationOption(op, "command-timeout")
	if err != nil {
		return invalidFailure(err), nil
	}
	if err := validateExecutionOptions(concurrency, timeout, op.Arguments); err != nil {
		return invalidFailure(err), nil
	}
	outputLimit, err := outputLimitOption(op)
	if err != nil {
		return invalidFailure(err), nil
	}
	selectors, err := stringsOption(op, "container")
	if err != nil {
		return invalidFailure(err), nil
	}
	runtimes, err := stringsOption(op, "runtime")
	if err != nil {
		return invalidFailure(err), nil
	}
	networkOnly, err := boolOption(op, "network-only")
	if err != nil {
		return invalidFailure(err), nil
	}
	scope, err := containerScopeOption(op, networkOnly)
	if err != nil {
		return invalidFailure(err), nil
	}
	selectedRuntime := map[string]bool{}
	for _, runtime := range runtimes {
		runtime = strings.ToLower(strings.TrimSpace(runtime))
		if runtime != "docker" && runtime != "containerd" && runtime != "cri-o" && runtime != "podman" && runtime != "container" {
			return invalidFailure(fmt.Errorf("unsupported runtime %q", runtime)), nil
		}
		selectedRuntime[runtime] = true
	}
	containers, err := discoverContainers(r.deps.Files)
	if err != nil {
		return core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{{Code: v1alpha1.ErrorExecutionFailed, Message: "container processes could not be discovered", Details: map[string]string{"reason": err.Error()}}}}, nil
	}
	containers, err = selectContainers(containers, selectors, selectedRuntime)
	if err != nil {
		return invalidFailure(err), nil
	}
	if len(containers) == 0 {
		return core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{{Code: "NO_CONTAINERS", Message: "no matching running containers were detected", Details: map[string]string{}}}, Errors: []v1alpha1.TargetError{}}, nil
	}
	nsenter, err := r.deps.Processes.LookPath("nsenter")
	if err != nil {
		return dependencyFailure("nsenter", err), nil
	}
	targets := make([]commandTarget, 0, len(containers))
	for _, container := range containers {
		args := containerNamespaceArguments(container.PID, scope)
		args = append(args, op.Arguments...)
		targets = append(targets, commandTarget{Name: container.ID, Runtime: container.Runtime, PID: container.PID, Scope: scope, Path: nsenter, Args: args})
	}
	return r.runTargets(ctx, targets, concurrency, timeout, outputLimit), nil
}

func containerScopeOption(op v1alpha1.Operation, networkOnly bool) (string, error) {
	scope, err := stringOption(op, "scope")
	if err != nil {
		return "", err
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		scope = "net"
	}
	if scope != "exec" && scope != "fs" && scope != "net" && scope != "process" {
		return "", fmt.Errorf("container scope must be net, exec, fs, or process")
	}
	if networkOnly && scope != "net" {
		return "", fmt.Errorf("--network-only cannot be combined with --scope %s", scope)
	}
	return scope, nil
}

func containerNamespaceArguments(pid int, scope string) []string {
	target := strconv.Itoa(pid)
	args := []string{"--target", target}
	switch scope {
	case "exec":
		args = append(args, "--mount", "--uts", "--ipc", "--net", "--pid", "--root=/proc/"+target+"/root", "--wd=/proc/"+target+"/root")
	case "fs":
		args = append(args, "--mount", "--root=/proc/"+target+"/root", "--wd=/proc/"+target+"/root")
	case "process":
		args = append(args, "--mount", "--pid", "--root=/proc/"+target+"/root", "--wd=/proc/"+target+"/root")
	default:
		// Retain the host filesystem so minimal containers can use host-installed
		// ip, ss, ping, and similar tools in the selected network namespace.
		args = append(args, "--net")
	}
	return append(args, "--")
}

func discoverContainers(files core.FileSystem) ([]containerProcess, error) {
	entries, err := files.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	byID := map[string]containerProcess{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		data, err := files.ReadFile("/proc/"+entry.Name()+"/cgroup", 1<<20)
		if err != nil {
			continue
		}
		id, runtime, cgroupPath := containerFromCgroup(string(data))
		if id == "" {
			continue
		}
		current, exists := byID[id]
		if !exists || pid < current.PID {
			byID[id] = containerProcess{ID: id, Runtime: runtime, CgroupPath: cgroupPath, PID: pid}
		}
	}
	result := make([]containerProcess, 0, len(byID))
	for _, item := range byID {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func containerFromCgroup(content string) (string, string, string) {
	for _, line := range strings.Split(strings.ToLower(content), "\n") {
		line = strings.TrimSpace(line)
		if separator := strings.LastIndexByte(line, ':'); separator >= 0 {
			line = line[separator+1:]
		}
		for _, candidate := range containerPatterns {
			if match := candidate.pattern.FindStringSubmatch(line); len(match) == 2 {
				return match[1], candidate.runtime, line
			}
		}
	}
	return "", "", ""
}

func selectContainers(containers []containerProcess, selectors []string, runtimes map[string]bool) ([]containerProcess, error) {
	result := []containerProcess{}
	matched := make([]int, len(selectors))
	for _, container := range containers {
		if len(runtimes) > 0 && !runtimes[container.Runtime] {
			continue
		}
		selected := len(selectors) == 0
		for index, selector := range selectors {
			selector = strings.ToLower(strings.TrimSpace(selector))
			if len(selector) < 4 {
				return nil, fmt.Errorf("container selector %q must contain at least 4 characters", selector)
			}
			if strings.HasPrefix(container.ID, selector) {
				selected = true
				matched[index]++
			}
		}
		if selected {
			result = append(result, container)
		}
	}
	for index, count := range matched {
		if count == 0 {
			return nil, fmt.Errorf("container selector %q did not match a running container", selectors[index])
		}
		if count > 1 {
			return nil, fmt.Errorf("container selector %q is ambiguous", selectors[index])
		}
	}
	return result, nil
}
