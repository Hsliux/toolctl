package doctor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	"toolctl/internal/executor"
	resultbuilder "toolctl/internal/result"
)

type fakeModule struct {
	name   string
	checks []v1alpha1.CheckResult
}

func (m fakeModule) Info() v1alpha1.ModuleInfo                      { return v1alpha1.ModuleInfo{Name: m.name} }
func (fakeModule) Capabilities() []v1alpha1.Capability              { return nil }
func (fakeModule) NewRunner(core.Dependencies) (core.Runner, error) { return nil, nil }
func (m fakeModule) Doctor(context.Context) []v1alpha1.CheckResult {
	return append([]v1alpha1.CheckResult(nil), m.checks...)
}

func TestDoctorAggregatesSortsAndFiltersComponents(t *testing.T) {
	runner := &Runner{sources: []core.Module{
		fakeModule{name: "zeta", checks: []v1alpha1.CheckResult{{Name: "b", Status: v1alpha1.CheckWarn, Details: map[string]string{}}}},
		fakeModule{name: "alpha", checks: []v1alpha1.CheckResult{{Name: "a", Status: v1alpha1.CheckPass, Details: map[string]string{}}}},
	}}
	output, err := runner.Execute(context.Background(), v1alpha1.Operation{Options: map[string]json.RawMessage{}})
	if err != nil || len(output.Items) != 2 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	if output.Items[0].Name != "a" {
		t.Fatalf("items = %#v", output.Items)
	}
	filtered, err := runner.Execute(context.Background(), v1alpha1.Operation{Options: map[string]json.RawMessage{"component": json.RawMessage(`["zeta"]`)}})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Name != "b" {
		t.Fatalf("output=%#v err=%v", filtered, err)
	}
}

func TestDoctorUsesPublicComponentNames(t *testing.T) {
	runner := &Runner{sources: []core.Module{
		fakeModule{name: "system", checks: []v1alpha1.CheckResult{{Name: "platform", Status: v1alpha1.CheckPass, Details: map[string]string{}}}},
		fakeModule{name: "performance", checks: []v1alpha1.CheckResult{{Name: "procfs", Status: v1alpha1.CheckPass, Details: map[string]string{}}}},
		fakeModule{name: "benchmark", checks: []v1alpha1.CheckResult{{Name: "fio", Status: v1alpha1.CheckWarn, Details: map[string]string{}}}},
	}}
	output, err := runner.Execute(context.Background(), v1alpha1.Operation{Options: map[string]json.RawMessage{}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"bench", "core", "perf"}
	for index, item := range output.Items {
		var check v1alpha1.CheckResult
		if err := json.Unmarshal(item.Data, &check); err != nil {
			t.Fatal(err)
		}
		if check.Details["component"] != want[index] {
			t.Fatalf("components: item %d = %q, want %q", index, check.Details["component"], want[index])
		}
	}
	filtered, err := runner.Execute(context.Background(), v1alpha1.Operation{Options: map[string]json.RawMessage{"component": json.RawMessage(`["core"]`)}})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Name != "platform" {
		t.Fatalf("filtered=%#v err=%v", filtered, err)
	}
}

func TestDoctorFailsOnFailAndStrictWarn(t *testing.T) {
	runner := &Runner{sources: []core.Module{fakeModule{name: "system", checks: []v1alpha1.CheckResult{
		{Name: "platform", Status: v1alpha1.CheckFail, Message: "unsupported", Details: map[string]string{}},
		{Name: "optional", Status: v1alpha1.CheckWarn, Message: "missing", Details: map[string]string{}},
	}}}}
	output, err := runner.Execute(context.Background(), v1alpha1.Operation{Options: map[string]json.RawMessage{}})
	if err != nil || len(output.Errors) != 1 || output.Errors[0].Target != "core/platform" {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	result := resultbuilder.Build(v1alpha1.Operation{ID: "doctor", Capability: capabilityDoctor}, output, nil, v1alpha1.ModuleInfo{Name: "doctor"}, time.Unix(0, 0), 0)
	if result.Status != v1alpha1.StatusPartial || executor.ExitCode(result) == 0 {
		t.Fatalf("result=%#v exit=%d", result, executor.ExitCode(result))
	}
	strict, err := runner.Execute(context.Background(), v1alpha1.Operation{Options: map[string]json.RawMessage{"strict": json.RawMessage("true")}})
	if err != nil || len(strict.Errors) != 2 {
		t.Fatalf("strict=%#v err=%v", strict, err)
	}
	strictResult := resultbuilder.Build(v1alpha1.Operation{ID: "doctor", Capability: capabilityDoctor}, strict, nil, v1alpha1.ModuleInfo{Name: "doctor"}, time.Unix(0, 0), 0)
	if strictResult.Status != v1alpha1.StatusFailure || executor.ExitCode(strictResult) == 0 {
		t.Fatalf("strict result=%#v exit=%d", strictResult, executor.ExitCode(strictResult))
	}
}
