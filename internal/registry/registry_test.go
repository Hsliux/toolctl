package registry

import (
	"context"
	"testing"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

type fakeModule struct {
	name string
	cap  v1alpha1.Capability
}

func (m fakeModule) Info() v1alpha1.ModuleInfo                        { return v1alpha1.ModuleInfo{Name: m.name} }
func (m fakeModule) Capabilities() []v1alpha1.Capability              { return []v1alpha1.Capability{m.cap} }
func (m fakeModule) NewRunner(core.Dependencies) (core.Runner, error) { return fakeRunner{}, nil }
func (m fakeModule) Doctor(context.Context) []v1alpha1.CheckResult    { return nil }

type fakeRunner struct{}

func (fakeRunner) Execute(context.Context, v1alpha1.Operation) (core.RunOutput, error) {
	return core.RunOutput{}, nil
}

func TestRejectsCommandPathConflict(t *testing.T) {
	one := fakeModule{name: "one", cap: testCapability("one.item.list", "host")}
	two := fakeModule{name: "two", cap: testCapability("two.item.list", "host")}
	if _, err := New([]core.Module{one, two}, core.Dependencies{}); err == nil {
		t.Fatal("expected path conflict")
	}
}

func TestCapabilitiesAreSortedByCommandPath(t *testing.T) {
	one := fakeModule{name: "one", cap: testCapability("one.item.list", "zeta")}
	two := fakeModule{name: "two", cap: testCapability("two.item.list", "alpha")}
	r, err := New([]core.Module{one, two}, core.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	caps := r.Capabilities()
	if caps[0].ID != "two.item.list" {
		t.Fatalf("first capability = %s", caps[0].ID)
	}
}

func TestRejectsDuplicateAndReservedOptionShorthands(t *testing.T) {
	duplicate := testCapability("one.item.list", "one")
	duplicate.Options = []v1alpha1.OptionSpec{{Name: "interval", Shorthand: "i", Type: v1alpha1.OptionDuration}, {Name: "input", Shorthand: "i", Type: v1alpha1.OptionString}}
	if _, err := New([]core.Module{fakeModule{name: "one", cap: duplicate}}, core.Dependencies{}); err == nil {
		t.Fatal("expected duplicate shorthand error")
	}
	reserved := testCapability("two.item.list", "two")
	reserved.Options = []v1alpha1.OptionSpec{{Name: "output-file", Shorthand: "o", Type: v1alpha1.OptionString}}
	if _, err := New([]core.Module{fakeModule{name: "two", cap: reserved}}, core.Dependencies{}); err == nil {
		t.Fatal("expected reserved shorthand error")
	}
}

func testCapability(id, path string) v1alpha1.Capability {
	return v1alpha1.Capability{ID: id, Command: v1alpha1.CommandPathSpec{Path: []string{path}}}
}
