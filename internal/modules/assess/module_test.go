package assess

import (
	"context"
	"encoding/json"
	"io/fs"
	"sync"
	"testing"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

type recordingRunner struct {
	mu    sync.Mutex
	calls []v1alpha1.Operation
}

func (r *recordingRunner) Execute(_ context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	r.mu.Lock()
	r.calls = append(r.calls, op)
	r.mu.Unlock()
	return core.RunOutput{Items: []v1alpha1.Item{{Kind: "FakeResult", Name: op.Capability, Data: json.RawMessage(`{}`)}}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
}

type recordingModule struct{ runner *recordingRunner }

func (recordingModule) Info() v1alpha1.ModuleInfo { return v1alpha1.ModuleInfo{Name: "fake"} }
func (recordingModule) Capabilities() []v1alpha1.Capability {
	ids := []string{"system.host.info", "raid.status", "system.performance.cpu", "benchmark.cpu.run", "benchmark.memory.run", "stability.burn.run", "benchmark.disk.run", "benchmark.network.run"}
	capabilities := make([]v1alpha1.Capability, 0, len(ids))
	for _, id := range ids {
		capabilities = append(capabilities, v1alpha1.Capability{ID: id})
	}
	return capabilities
}
func (m recordingModule) NewRunner(core.Dependencies) (core.Runner, error) { return m.runner, nil }
func (recordingModule) Doctor(context.Context) []v1alpha1.CheckResult {
	return []v1alpha1.CheckResult{{Name: "ready", Status: v1alpha1.CheckPass, Details: map[string]string{}}}
}

func TestQuickAssessmentRunsCoreSections(t *testing.T) {
	recorder := &recordingRunner{}
	module := New([]core.Module{recordingModule{runner: recorder}})
	runner, err := module.NewRunner(core.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	output, err := runner.Execute(context.Background(), v1alpha1.Operation{ID: "assess", Name: "quick", Options: map[string]json.RawMessage{}, TimeoutMS: 30000})
	if err != nil || len(output.Errors) != 0 || len(output.Items) != 7 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.calls) != 6 || recorder.calls[2].Capability != "system.performance.cpu" {
		t.Fatalf("calls=%#v", recorder.calls)
	}
}

func TestFullAssessmentMarksUnspecifiedExternalBenchmarksSkipped(t *testing.T) {
	recorder := &recordingRunner{}
	module := New([]core.Module{recordingModule{runner: recorder}})
	runner, err := module.NewRunner(core.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	output, err := runner.Execute(context.Background(), v1alpha1.Operation{ID: "assess", Name: "full", Options: map[string]json.RawMessage{}, TimeoutMS: 30000})
	if err != nil || len(output.Errors) != 0 || len(output.Items) != 10 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	skips := 0
	for _, item := range output.Items {
		var section Section
		if err := json.Unmarshal(item.Data, &section); err != nil {
			t.Fatal(err)
		}
		if section.Status == "skip" {
			skips++
		}
	}
	if skips != 2 {
		t.Fatalf("skip count=%d output=%#v", skips, output)
	}
}

type compareFS map[string][]byte

func (f compareFS) ReadFile(name string, _ int64) ([]byte, error) {
	value, ok := f[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return value, nil
}
func (compareFS) ReadDir(string) ([]fs.DirEntry, error) { return nil, fs.ErrNotExist }
func (compareFS) Stat(string) (fs.FileInfo, error)      { return nil, fs.ErrNotExist }
func (compareFS) StatFS(string) (core.FileSystemStats, error) {
	return core.FileSystemStats{}, fs.ErrNotExist
}

func TestAssessmentCompareFlagsOnlyRegressions(t *testing.T) {
	makeAssessment := func(throughput, latency float64) []byte {
		nested := v1alpha1.Item{Kind: "FakeBenchmark", Name: "cpu", Data: json.RawMessage([]byte(`{"bytesPerSecond":` + numberJSON(throughput) + `,"p99Micros":` + numberJSON(latency) + `}`))}
		section := Section{Section: "cpu", Status: "pass", Items: []v1alpha1.Item{nested}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
		sectionData, _ := json.Marshal(section)
		result := v1alpha1.Result{APIVersion: v1alpha1.APIVersion, Kind: "Result", Items: []v1alpha1.Item{{Kind: "AssessmentSection", Name: "cpu", Data: sectionData}}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
		data, _ := json.Marshal(result)
		return data
	}
	runner := &Runner{files: compareFS{"base.json": makeAssessment(100, 100), "now.json": makeAssessment(120, 120)}}
	op := v1alpha1.Operation{Capability: capabilityCompare, Options: map[string]json.RawMessage{"baseline": json.RawMessage(`"base.json"`), "current": json.RawMessage(`"now.json"`), "warn-percent": json.RawMessage(`10`), "fail-percent": json.RawMessage(`25`)}}
	output, err := runner.Execute(context.Background(), op)
	if err != nil || len(output.Items) != 2 || len(output.Errors) != 0 || len(output.Warnings) != 1 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
}

func numberJSON(value float64) string { data, _ := json.Marshal(value); return string(data) }
