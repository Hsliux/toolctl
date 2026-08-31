package result

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

func TestBuildStatusAndCollections(t *testing.T) {
	op := v1alpha1.Operation{ID: "op-1", Capability: "test.item.list"}
	item, err := NewItem("TestItem", "one", "", struct {
		Count uint64 `json:"count"`
	}{Count: 9_007_199_254_740_993})
	if err != nil {
		t.Fatal(err)
	}
	result := Build(op, core.RunOutput{Items: []v1alpha1.Item{item}}, nil, v1alpha1.ModuleInfo{Name: "test", Version: "1"}, time.Unix(1, 0), 15*time.Millisecond)
	if result.Status != v1alpha1.StatusSuccess {
		t.Fatalf("status = %s", result.Status)
	}
	if result.Warnings == nil || result.Errors == nil {
		t.Fatal("empty collections must be non-nil")
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(item.Data) != `{"count":9007199254740993}` {
		t.Fatalf("integer precision changed: %s", item.Data)
	}
	if !json.Valid(data) {
		t.Fatal("result is not valid JSON")
	}
}

func TestBuildPartialAndFailure(t *testing.T) {
	op := v1alpha1.Operation{ID: "op-1", Capability: "test.item.list"}
	item, _ := NewItem("TestItem", "one", "", struct{}{})
	output := core.RunOutput{Items: []v1alpha1.Item{item}, Errors: []v1alpha1.TargetError{{Code: v1alpha1.ErrorTimeout, Message: "late"}}}
	partial := Build(op, output, nil, v1alpha1.ModuleInfo{}, time.Time{}, 0)
	if partial.Status != v1alpha1.StatusPartial {
		t.Fatalf("partial status = %s", partial.Status)
	}
	failure := Build(op, core.RunOutput{}, errors.New("boom"), v1alpha1.ModuleInfo{}, time.Time{}, 0)
	if failure.Status != v1alpha1.StatusFailure || len(failure.Errors) != 1 {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestBuildFailureWhenEveryTargetItemFailed(t *testing.T) {
	op := v1alpha1.Operation{ID: "op-1", Capability: "batch.ssh.execute"}
	one, _ := NewItem("CommandResult", "one", "one", struct{}{})
	two, _ := NewItem("CommandResult", "two", "two", struct{}{})
	allFailed := Build(op, core.RunOutput{Items: []v1alpha1.Item{one, two}, Errors: []v1alpha1.TargetError{{Target: "one", Code: v1alpha1.ErrorExecutionFailed}, {Target: "two", Code: v1alpha1.ErrorExecutionFailed}}}, nil, v1alpha1.ModuleInfo{}, time.Time{}, 0)
	if allFailed.Status != v1alpha1.StatusFailure {
		t.Fatalf("status = %s", allFailed.Status)
	}
	mixed := Build(op, core.RunOutput{Items: []v1alpha1.Item{one, two}, Errors: []v1alpha1.TargetError{{Target: "one", Code: v1alpha1.ErrorExecutionFailed}}}, nil, v1alpha1.ModuleInfo{}, time.Time{}, 0)
	if mixed.Status != v1alpha1.StatusPartial {
		t.Fatalf("status = %s", mixed.Status)
	}
}
