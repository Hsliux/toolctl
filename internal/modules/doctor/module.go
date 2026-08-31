package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

const capabilityDoctor = "core.environment.doctor"

type Module struct{ sources []core.Module }

func New(sources []core.Module) *Module {
	return &Module{sources: append([]core.Module(nil), sources...)}
}

func (*Module) Info() v1alpha1.ModuleInfo {
	return v1alpha1.ModuleInfo{Name: "doctor", Version: "0.1.0", MaxConcurrency: 1}
}

func (*Module) Capabilities() []v1alpha1.Capability {
	return []v1alpha1.Capability{{
		ID: capabilityDoctor, Domain: "core", Resource: "environment", Verb: "doctor",
		Command: v1alpha1.CommandPathSpec{Path: []string{"doctor"}}, Summary: "Check the local environment and optional command dependencies",
		Options: []v1alpha1.OptionSpec{
			{Name: "component", Type: v1alpha1.OptionStringSlice, Description: "Only show selected components"},
			{Name: "strict", Type: v1alpha1.OptionBool, Description: "Return a failure when any check is warn or fail"},
		},
		Columns: []v1alpha1.ColumnHint{
			{Header: "COMPONENT", Path: "data.details.component", Type: v1alpha1.ColumnString, Order: 10},
			{Header: "CHECK", Path: "data.name", Type: v1alpha1.ColumnString, Order: 20},
			{Header: "STATUS", Path: "data.status", Type: v1alpha1.ColumnString, Order: 30},
			{Header: "MESSAGE", Path: "data.message", Type: v1alpha1.ColumnString, Order: 40},
			{Header: "PATH", Path: "data.details.path", Type: v1alpha1.ColumnString, Wide: true, Order: 50},
			{Header: "VERSION", Path: "data.details.version", Type: v1alpha1.ColumnString, Wide: true, Order: 60},
			{Header: "CAPABILITY", Path: "data.details.capability", Type: v1alpha1.ColumnString, Wide: true, Order: 70},
			{Header: "SCOPE", Path: "data.details.scope", Type: v1alpha1.ColumnString, Wide: true, Order: 80},
			{Header: "CONFIG", Path: "data.details.config", Type: v1alpha1.ColumnString, Wide: true, Order: 90},
			{Header: "SUGGESTION", Path: "data.suggestion", Type: v1alpha1.ColumnString, Wide: true, Order: 100},
		},
	}}
}

func (m *Module) NewRunner(core.Dependencies) (core.Runner, error) {
	return &Runner{sources: m.sources}, nil
}
func (*Module) Doctor(context.Context) []v1alpha1.CheckResult { return []v1alpha1.CheckResult{} }

type Runner struct{ sources []core.Module }

func publicComponent(name string) string {
	switch name {
	case "system":
		return "core"
	case "performance":
		return "perf"
	case "benchmark":
		return "bench"
	default:
		return name
	}
}

func (r *Runner) Execute(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	strict := false
	if raw, ok := op.Options["strict"]; ok {
		if err := json.Unmarshal(raw, &strict); err != nil {
			return core.RunOutput{}, fmt.Errorf("invalid --strict: %w", err)
		}
	}
	selected := map[string]bool{}
	if raw, ok := op.Options["component"]; ok {
		var values []string
		if err := json.Unmarshal(raw, &values); err != nil {
			return core.RunOutput{}, fmt.Errorf("invalid --component: %w", err)
		}
		for _, value := range values {
			selected[strings.ToLower(strings.TrimSpace(value))] = true
		}
	}
	type namedCheck struct {
		component string
		check     v1alpha1.CheckResult
	}
	checks := []namedCheck{}
	known := map[string]bool{}
	for _, source := range r.sources {
		component := publicComponent(source.Info().Name)
		known[component] = true
		if len(selected) > 0 && !selected[component] {
			continue
		}
		for _, check := range source.Doctor(ctx) {
			if check.Details == nil {
				check.Details = map[string]string{}
			}
			check.Details["component"] = component
			checks = append(checks, namedCheck{component: component, check: check})
		}
	}
	for component := range selected {
		if !known[component] {
			return core.RunOutput{}, fmt.Errorf("unknown doctor component %q", component)
		}
	}
	sort.SliceStable(checks, func(i, j int) bool {
		if checks[i].component != checks[j].component {
			return checks[i].component < checks[j].component
		}
		return checks[i].check.Name < checks[j].check.Name
	})
	items := []v1alpha1.Item{}
	errorsOut := []v1alpha1.TargetError{}
	for _, entry := range checks {
		target := entry.component + "/" + entry.check.Name
		item, err := resultbuilder.NewItem("EnvironmentCheck", entry.check.Name, target, entry.check)
		if err != nil {
			return core.RunOutput{}, err
		}
		items = append(items, item)
		if entry.check.Status == v1alpha1.CheckFail || strict && entry.check.Status == v1alpha1.CheckWarn {
			errorsOut = append(errorsOut, v1alpha1.TargetError{
				Target: target, Code: v1alpha1.ErrorExecutionFailed,
				Message: "environment check " + string(entry.check.Status) + ": " + entry.check.Message,
				Details: map[string]string{"component": entry.component, "check": entry.check.Name, "status": string(entry.check.Status)},
			})
		}
	}
	return core.RunOutput{Items: items, Warnings: []v1alpha1.Diagnostic{}, Errors: errorsOut}, nil
}
