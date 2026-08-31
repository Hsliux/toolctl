package batch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

const (
	defaultCommandOutput = 128 << 10
	minCommandOutput     = 1 << 10
	maxCommandOutput     = 16 << 20
)

type commandTarget struct {
	Name    string
	Runtime string
	PID     int
	Scope   string
	Path    string
	Args    []string
}

type CommandResult struct {
	Target          string `json:"target"`
	Runtime         string `json:"runtime,omitempty"`
	PID             int    `json:"pid,omitempty"`
	Scope           string `json:"scope,omitempty"`
	Status          string `json:"status"`
	ExitCode        int    `json:"exitCode"`
	DurationMS      int64  `json:"durationMs"`
	Summary         string `json:"summary,omitempty"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	TimedOut        bool   `json:"timedOut"`
	StdoutTruncated bool   `json:"stdoutTruncated"`
	StderrTruncated bool   `json:"stderrTruncated"`
	OutputTruncated bool   `json:"outputTruncated"`
}

func (r *Runner) Execute(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	switch op.Capability {
	case capabilityContainerStats:
		return r.containerStats(ctx, op)
	case capabilitySSH:
		return r.executeSSH(ctx, op)
	case capabilityContainers:
		return r.executeContainers(ctx, op)
	default:
		return core.RunOutput{}, fmt.Errorf("batch module does not support %s", op.Capability)
	}
}

func (r *Runner) runTargets(ctx context.Context, targets []commandTarget, concurrency int, timeout time.Duration, outputLimit int64) core.RunOutput {
	type indexedResult struct {
		index int
		value CommandResult
	}
	jobs := make(chan int)
	results := make(chan indexedResult, len(targets))
	workers := concurrency
	if workers > len(targets) {
		workers = len(targets)
	}
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				target := targets[index]
				targetCtx, cancel := context.WithTimeout(ctx, timeout)
				process := r.deps.Processes.Run(targetCtx, core.ProcessSpec{Path: target.Path, Args: target.Args, MaxStdout: outputLimit, MaxStderr: outputLimit})
				cancel()
				status := "success"
				if process.TimedOut {
					status = "timeout"
				} else if process.StdoutExceeded || process.StderrExceeded {
					status = "output-limit"
				} else if process.Err != nil || process.ExitCode != 0 {
					status = "failed"
				}
				stdout, stderr := strings.TrimSpace(string(process.Stdout)), strings.TrimSpace(string(process.Stderr))
				summary := firstLine(stdout)
				if summary == "" {
					summary = firstLine(stderr)
				}
				results <- indexedResult{index: index, value: CommandResult{Target: target.Name, Runtime: target.Runtime, PID: target.PID, Scope: target.Scope, Status: status, ExitCode: process.ExitCode, DurationMS: process.Duration.Milliseconds(), Summary: summary, Stdout: stdout, Stderr: stderr, TimedOut: process.TimedOut, StdoutTruncated: process.StdoutExceeded, StderrTruncated: process.StderrExceeded, OutputTruncated: process.StdoutExceeded || process.StderrExceeded}}
			}
		}()
	}
	go func() {
		for index := range targets {
			jobs <- index
		}
		close(jobs)
		group.Wait()
		close(results)
	}()
	ordered := make([]CommandResult, len(targets))
	for result := range results {
		ordered[result.index] = result.value
	}
	output := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
	for _, value := range ordered {
		item, err := resultbuilder.NewItem("BatchCommandResult", value.Target, value.Target, value)
		if err != nil {
			output.Errors = append(output.Errors, v1alpha1.TargetError{Target: value.Target, Code: v1alpha1.ErrorInternal, Message: err.Error(), Details: map[string]string{}})
			continue
		}
		output.Items = append(output.Items, item)
		if value.Status != "success" {
			code := v1alpha1.ErrorExecutionFailed
			if value.TimedOut {
				code = v1alpha1.ErrorTimeout
			} else if value.OutputTruncated {
				code = v1alpha1.ErrorOutputLimitExceeded
			}
			details := map[string]string{"exitCode": fmt.Sprint(value.ExitCode), "stderr": limitedDetail(value.Stderr)}
			if value.OutputTruncated {
				details["suggestion"] = "reduce command output or increase --max-output"
			}
			output.Errors = append(output.Errors, v1alpha1.TargetError{Target: value.Target, Code: code, Message: "batch command " + value.Status, Retryable: value.TimedOut, Details: details})
		}
	}
	return output
}

func firstLine(value string) string {
	if before, _, found := strings.Cut(value, "\n"); found {
		value = before
	}
	value = strings.TrimSpace(value)
	if len(value) > 160 {
		value = value[:157] + "..."
	}
	return value
}

func limitedDetail(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1024 {
		return value[:1021] + "..."
	}
	return value
}

func dependencyFailure(name string, err error) core.RunOutput {
	return core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{{Code: v1alpha1.ErrorDependencyMissing, Message: name + " is required", Details: map[string]string{"reason": err.Error()}}}}
}

func invalidFailure(err error) core.RunOutput {
	return core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{{Code: v1alpha1.ErrorInvalidArgument, Message: err.Error(), Details: map[string]string{}}}}
}

func sortTargets(targets []commandTarget) {
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
}
