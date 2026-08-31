package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"toolctl/api/v1alpha1"
	"toolctl/internal/render"
)

func TestRootHelpGroupsCommandsByOperatorWorkflow(t *testing.T) {
	capabilities := []v1alpha1.Capability{
		{ID: "system.host.info", Command: v1alpha1.CommandPathSpec{Path: []string{"host"}}},
		{ID: "system.performance.cpu", Command: v1alpha1.CommandPathSpec{Path: []string{"perf", "cpu"}}},
		{ID: "raid.status", Command: v1alpha1.CommandPathSpec{Path: []string{"raid", "status"}}},
		{ID: "batch.ssh", Command: v1alpha1.CommandPathSpec{Path: []string{"batch", "ssh"}}},
		{ID: "database.mysql.status", Command: v1alpha1.CommandPathSpec{Path: []string{"mysql"}}},
		{ID: "benchmark.cpu", Command: v1alpha1.CommandPathSpec{Path: []string{"bench", "cpu"}}},
	}
	var stdout, stderr bytes.Buffer
	command, err := New(BuildInfo{}, capabilities, fixedID("op-fixed"), func(context.Context, v1alpha1.Operation, render.Options) (int, error) {
		return 0, nil
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	command.SetArgs([]string{"--help"})
	if _, err := command.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	help := stdout.String()
	for _, expected := range []string{
		"toolctl is a unified Linux server inspection and operations CLI.",
		"Server Information Commands:",
		"Troubleshooting and Performance Commands:",
		"Storage and RAID Commands:",
		"Container and Batch Commands:",
		"Database Commands:",
		"Benchmark and Assessment Commands:",
		"Configuration and Other Commands:",
		"Examples:",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("root help missing %q:\n%s", expected, help)
		}
	}
	if strings.Contains(help, "Available Commands:") {
		t.Fatalf("root help unexpectedly contains an ungrouped command section:\n%s", help)
	}
}

func TestIntermediateCommandUsesSpecificSummary(t *testing.T) {
	capability := v1alpha1.Capability{ID: "system.performance.cpu", Command: v1alpha1.CommandPathSpec{Path: []string{"perf", "cpu"}}}
	var stdout, stderr bytes.Buffer
	command, err := New(BuildInfo{}, []v1alpha1.Capability{capability}, fixedID("op-fixed"), func(context.Context, v1alpha1.Operation, render.Options) (int, error) {
		return 0, nil
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	command.SetArgs([]string{"perf", "--help"})
	if _, err := command.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Inspect live and historical system performance") || strings.Contains(stdout.String(), "Commands for perf") {
		t.Fatalf("unexpected perf help:\n%s", stdout.String())
	}
}

type fixedID string

func (id fixedID) NewID() string { return string(id) }

func TestShortHostCommandBuildsStableCapability(t *testing.T) {
	capability := v1alpha1.Capability{
		ID: "system.host.info", Command: v1alpha1.CommandPathSpec{Path: []string{"host"}},
		NameMode: v1alpha1.NameNone, TargetMode: v1alpha1.TargetNone,
	}
	called := false
	var stdout, stderr bytes.Buffer
	command, err := New(BuildInfo{}, []v1alpha1.Capability{capability}, fixedID("op-fixed"), func(_ context.Context, op v1alpha1.Operation, options render.Options) (int, error) {
		called = true
		if op.ID != "op-fixed" || op.Capability != "system.host.info" {
			t.Fatalf("operation = %#v", op)
		}
		if options.Format != render.FormatJSON {
			t.Fatalf("format = %s", options.Format)
		}
		return 0, nil
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	command.SetArgs([]string{"host", "-o", "json"})
	code, err := command.Execute(context.Background())
	if err != nil || code != 0 || !called {
		t.Fatalf("code=%d called=%v err=%v", code, called, err)
	}
}

func TestUnknownOutputIsArgumentError(t *testing.T) {
	capability := v1alpha1.Capability{ID: "system.host.info", Command: v1alpha1.CommandPathSpec{Path: []string{"host"}}}
	command, err := New(BuildInfo{}, []v1alpha1.Capability{capability}, fixedID("op-fixed"), func(context.Context, v1alpha1.Operation, render.Options) (int, error) {
		return 0, nil
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	command.SetArgs([]string{"host", "-o", "xml"})
	_, err = command.Execute(context.Background())
	if err == nil {
		t.Fatal("expected output format error")
	}
}

func TestCapabilityOptionsUseStableJSONTypes(t *testing.T) {
	capability := v1alpha1.Capability{
		ID: "test.job.run", Command: v1alpha1.CommandPathSpec{Path: []string{"job"}},
		Options: []v1alpha1.OptionSpec{
			{Name: "count", Shorthand: "c", Type: v1alpha1.OptionInt},
			{Name: "wait", Shorthand: "w", Type: v1alpha1.OptionDuration},
			{Name: "labels", Type: v1alpha1.OptionStringSlice},
		},
	}
	var received v1alpha1.Operation
	command, err := New(BuildInfo{}, []v1alpha1.Capability{capability}, fixedID("op-fixed"), func(_ context.Context, op v1alpha1.Operation, _ render.Options) (int, error) {
		received = op
		return 0, nil
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	command.SetArgs([]string{"job", "-c", "3", "-w", "1500ms", "--labels", "a,b"})
	if _, err := command.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if string(received.Options["count"]) != "3" || string(received.Options["wait"]) != "1500" || string(received.Options["labels"]) != `["a","b"]` {
		t.Fatalf("options = %#v", received.Options)
	}
}

func TestLiveOptionCreatesUnboundedOperation(t *testing.T) {
	capability := v1alpha1.Capability{
		ID: "system.performance.cpu", Command: v1alpha1.CommandPathSpec{Path: []string{"perf", "cpu"}},
		Options: []v1alpha1.OptionSpec{{Name: "live", Type: v1alpha1.OptionBool}},
	}
	var received v1alpha1.Operation
	command, err := New(BuildInfo{}, []v1alpha1.Capability{capability}, fixedID("op-fixed"), func(_ context.Context, op v1alpha1.Operation, _ render.Options) (int, error) {
		received = op
		return 0, nil
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	command.SetArgs([]string{"perf", "cpu", "--live"})
	if _, err := command.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if received.TimeoutMS != 0 || string(received.Options["live"]) != "true" {
		t.Fatalf("operation = %#v", received)
	}
}

func TestPassthroughArgumentsArePreserved(t *testing.T) {
	capability := v1alpha1.Capability{ID: "batch.container.execute", Command: v1alpha1.CommandPathSpec{Path: []string{"batch", "containers"}}, PassthroughArgs: true}
	var received v1alpha1.Operation
	command, err := New(BuildInfo{}, []v1alpha1.Capability{capability}, fixedID("op-fixed"), func(_ context.Context, op v1alpha1.Operation, _ render.Options) (int, error) {
		received = op
		return 0, nil
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	command.SetArgs([]string{"batch", "containers", "--", "sh", "-c", "echo ok | wc -c"})
	if _, err := command.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"sh", "-c", "echo ok | wc -c"}
	if len(received.Arguments) != len(want) {
		t.Fatalf("arguments = %#v", received.Arguments)
	}
	if received.Name != "" {
		t.Fatalf("passthrough command leaked into operation name: %q", received.Name)
	}
	for index := range want {
		if received.Arguments[index] != want[index] {
			t.Fatalf("arguments = %#v", received.Arguments)
		}
	}
}
