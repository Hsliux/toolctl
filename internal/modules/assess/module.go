package assess

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

const capabilityAssess = "assessment.server.run"
const capabilityCompare = "assessment.result.compare"

type Module struct {
	sources []core.Module
}

func New(sources []core.Module) *Module {
	return &Module{sources: append([]core.Module(nil), sources...)}
}
func (*Module) Info() v1alpha1.ModuleInfo {
	return v1alpha1.ModuleInfo{Name: "assessment", Version: "0.1.0", MaxConcurrency: 1}
}
func (*Module) Doctor(context.Context) []v1alpha1.CheckResult { return nil }

func (*Module) Capabilities() []v1alpha1.Capability {
	return []v1alpha1.Capability{{
		ID: capabilityAssess, Domain: "assessment", Resource: "server", Verb: "run",
		Command: v1alpha1.CommandPathSpec{Path: []string{"assess"}}, Summary: "Run a quick or full new-server assessment",
		NameMode: v1alpha1.NameOptional, Mutating: true,
		Options: []v1alpha1.OptionSpec{
			{Name: "disk", Type: v1alpha1.OptionString, Description: "Mounted data directory for the full disk benchmark"},
			{Name: "network-peer", Type: v1alpha1.OptionString, Description: "toolctl bench net server peer for the full network benchmark"},
		}, Columns: []v1alpha1.ColumnHint{
			{Header: "SECTION", Path: "data.section", Type: v1alpha1.ColumnString, Order: 10},
			{Header: "STATUS", Path: "data.status", Type: v1alpha1.ColumnString, Order: 20},
			{Header: "SUMMARY", Path: "data.summary", Type: v1alpha1.ColumnString, Order: 30},
			{Header: "ITEMS", Path: "data.items", Type: v1alpha1.ColumnCount, Wide: true, Order: 40},
		},
	}, {
		ID: capabilityCompare, Domain: "assessment", Resource: "result", Verb: "compare",
		Command: v1alpha1.CommandPathSpec{Path: []string{"assess", "compare"}}, Summary: "Compare numeric metrics in two saved assessment JSON results",
		Options: []v1alpha1.OptionSpec{
			{Name: "baseline", Type: v1alpha1.OptionString, Required: true, Description: "Baseline assessment JSON file"},
			{Name: "current", Type: v1alpha1.OptionString, Required: true, Description: "Current assessment JSON file"},
			{Name: "warn-percent", Type: v1alpha1.OptionInt, Default: json.RawMessage("10"), Description: "Performance regression percentage that produces warn"},
			{Name: "fail-percent", Type: v1alpha1.OptionInt, Default: json.RawMessage("25"), Description: "Performance regression percentage that produces fail"},
		}, Columns: []v1alpha1.ColumnHint{
			{Header: "METRIC", Path: "data.metric", Type: v1alpha1.ColumnString, Order: 10},
			{Header: "BASELINE", Path: "data.baseline", Type: v1alpha1.ColumnDecimal, Order: 20},
			{Header: "CURRENT", Path: "data.current", Type: v1alpha1.ColumnDecimal, Order: 30},
			{Header: "CHANGE", Path: "data.changePercent", Type: v1alpha1.ColumnPercent, Order: 40},
			{Header: "STATUS", Path: "data.status", Type: v1alpha1.ColumnString, Order: 50},
		},
	}}
}

func (m *Module) NewRunner(deps core.Dependencies) (core.Runner, error) {
	runners := map[string]core.Runner{}
	for _, source := range m.sources {
		runner, err := source.NewRunner(deps)
		if err != nil {
			return nil, fmt.Errorf("assessment source %s: %w", source.Info().Name, err)
		}
		for _, capability := range source.Capabilities() {
			runners[capability.ID] = runner
		}
	}
	return &Runner{sources: m.sources, runners: runners, files: deps.Files}, nil
}

type Runner struct {
	sources []core.Module
	runners map[string]core.Runner
	files   core.FileSystem
}

type Section struct {
	Section  string                 `json:"section"`
	Status   string                 `json:"status"`
	Summary  string                 `json:"summary"`
	Items    []v1alpha1.Item        `json:"items"`
	Warnings []v1alpha1.Diagnostic  `json:"warnings"`
	Errors   []v1alpha1.TargetError `json:"errors"`
}

func (r *Runner) Execute(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	if op.Capability == capabilityCompare {
		return r.compare(op)
	}
	mode := strings.ToLower(strings.TrimSpace(op.Name))
	if mode == "" {
		mode = "quick"
	}
	if mode != "quick" && mode != "full" {
		return assessmentFailure(v1alpha1.ErrorInvalidArgument, "assessment mode must be quick or full"), nil
	}
	sections := []Section{r.environmentSection(ctx)}
	type sectionRun struct {
		name, capability string
		options          map[string]json.RawMessage
		skipReason       string
	}
	runs := []sectionRun{
		{name: "host", capability: "system.host.info", options: map[string]json.RawMessage{}},
		{name: "raid", capability: "raid.status", options: map[string]json.RawMessage{}},
		{name: "idle-cpu", capability: "system.performance.cpu", options: map[string]json.RawMessage{"interval": json.RawMessage("1000"), "count": json.RawMessage("1"), "live": json.RawMessage("false")}},
		{name: "cpu-single", capability: "benchmark.cpu.run", options: map[string]json.RawMessage{"duration": json.RawMessage("2000"), "threads": json.RawMessage("1")}},
		{name: "cpu-all", capability: "benchmark.cpu.run", options: map[string]json.RawMessage{"duration": json.RawMessage("2000"), "threads": json.RawMessage("0")}},
		{name: "memory", capability: "benchmark.memory.run", options: map[string]json.RawMessage{"size": json.RawMessage(`"64M"`), "duration": json.RawMessage("1000"), "threads": json.RawMessage("1")}},
	}
	if mode == "full" {
		runs = append(runs, sectionRun{"stability", "stability.burn.run", map[string]json.RawMessage{"duration": json.RawMessage("3000"), "threads": json.RawMessage("0"), "size": json.RawMessage(`"64M"`), "target": json.RawMessage(`"mixed"`)}, ""})
		disk, err := stringOption(op, "disk")
		if err != nil {
			return assessmentFailure(v1alpha1.ErrorInvalidArgument, err.Error()), nil
		}
		peer, err := stringOption(op, "network-peer")
		if err != nil {
			return assessmentFailure(v1alpha1.ErrorInvalidArgument, err.Error()), nil
		}
		if disk == "" {
			runs = append(runs, sectionRun{name: "disk", skipReason: "no --disk directory specified"})
		} else {
			runs = append(runs, sectionRun{name: "disk", capability: "benchmark.disk.run", options: diskOptions()})
			runs[len(runs)-1].options["target"] = json.RawMessage(strconvQuote(disk))
		}
		if peer == "" {
			runs = append(runs, sectionRun{name: "network", skipReason: "no --network-peer specified"})
		} else {
			runs = append(runs, sectionRun{name: "network", capability: "benchmark.network.run", options: map[string]json.RawMessage{"duration": json.RawMessage("3000"), "port": json.RawMessage("9234"), "target": json.RawMessage(strconvQuote(peer))}})
		}
	}
	for _, spec := range runs {
		if spec.skipReason != "" {
			sections = append(sections, skippedSection(spec.name, spec.skipReason))
			continue
		}
		name := ""
		if raw, ok := spec.options["target"]; ok {
			_ = json.Unmarshal(raw, &name)
			delete(spec.options, "target")
		}
		sections = append(sections, r.runSection(ctx, op, spec.name, spec.capability, name, spec.options))
	}
	output := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
	for _, section := range sections {
		item, err := resultbuilder.NewItem("AssessmentSection", section.Section, section.Section, section)
		if err != nil {
			return core.RunOutput{}, err
		}
		output.Items = append(output.Items, item)
		if section.Status == "fail" {
			output.Errors = append(output.Errors, v1alpha1.TargetError{Target: section.Section, Code: v1alpha1.ErrorExecutionFailed, Message: section.Summary, Details: map[string]string{"section": section.Section}})
		}
	}
	return output, nil
}

func (r *Runner) environmentSection(ctx context.Context) Section {
	checks := []v1alpha1.Item{}
	warns, fails := 0, 0
	for _, source := range r.sources {
		for _, check := range source.Doctor(ctx) {
			item, err := resultbuilder.NewItem("EnvironmentCheck", check.Name, "", check)
			if err == nil {
				checks = append(checks, item)
			}
			if check.Status == v1alpha1.CheckFail {
				fails++
			} else if check.Status == v1alpha1.CheckWarn {
				warns++
			}
		}
	}
	status := "pass"
	if fails > 0 {
		status = "fail"
	} else if warns > 0 {
		status = "warn"
	}
	return Section{Section: "environment", Status: status, Summary: fmt.Sprintf("%d checks, %d warnings, %d failures", len(checks), warns, fails), Items: checks, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
}

func (r *Runner) runSection(ctx context.Context, parent v1alpha1.Operation, section, capability, name string, options map[string]json.RawMessage) Section {
	runner, ok := r.runners[capability]
	if !ok {
		return Section{Section: section, Status: "skip", Summary: "capability is unavailable", Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
	}
	child := v1alpha1.Operation{APIVersion: v1alpha1.APIVersion, ID: parent.ID + "-" + section, Capability: capability, Name: name, Options: options, Targets: []v1alpha1.Target{}, Arguments: []string{}, TimeoutMS: parent.TimeoutMS}
	output, err := runner.Execute(ctx, child)
	if err != nil {
		return Section{Section: section, Status: "fail", Summary: err.Error(), Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{{Code: v1alpha1.ErrorExecutionFailed, Message: err.Error(), Details: map[string]string{}}}}
	}
	status := "pass"
	if len(output.Errors) > 0 {
		status = "fail"
	} else if len(output.Warnings) > 0 {
		status = "warn"
	}
	summary := fmt.Sprintf("%d items", len(output.Items))
	if len(output.Errors) > 0 {
		summary = output.Errors[0].Message
	} else if len(output.Warnings) > 0 {
		summary = output.Warnings[0].Message
	}
	return Section{Section: section, Status: status, Summary: summary, Items: output.Items, Warnings: output.Warnings, Errors: output.Errors}
}

func skippedSection(name, reason string) Section {
	return Section{Section: name, Status: "skip", Summary: reason, Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
}

func stringOption(op v1alpha1.Operation, name string) (string, error) {
	raw, ok := op.Options[name]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("invalid --%s: %w", name, err)
	}
	return strings.TrimSpace(value), nil
}

func diskOptions() map[string]json.RawMessage {
	return map[string]json.RawMessage{"profile": json.RawMessage(`"rand-write"`), "size": json.RawMessage(`"1G"`), "duration": json.RawMessage("10000"), "warmup": json.RawMessage("2000"), "jobs": json.RawMessage("1"), "depth": json.RawMessage("16"), "block-size": json.RawMessage(`"auto"`), "keep-file": json.RawMessage("false"), "dry-run": json.RawMessage("false"), "force": json.RawMessage("false")}
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func assessmentFailure(code v1alpha1.ErrorCode, message string) core.RunOutput {
	return core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{{Code: code, Message: message, Details: map[string]string{}}}}
}
