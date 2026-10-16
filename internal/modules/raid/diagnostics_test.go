package raid

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	"toolctl/internal/executor"
	"toolctl/internal/registry"
)

type runProcess struct {
	run func(context.Context, core.ProcessSpec) core.ProcessResult
}

func (*runProcess) LookPath(string) (string, error) { return "", os.ErrNotExist }
func (p *runProcess) Run(ctx context.Context, s core.ProcessSpec) core.ProcessResult {
	return p.run(ctx, s)
}

type raidClock struct{}

func (raidClock) Now() time.Time { return time.Unix(100, 0) }

func TestLocatorCleanupFailureSurvivesCancellation(t *testing.T) {
	base := &locateProcesses{}
	p := &runProcess{run: func(ctx context.Context, s core.ProcessSpec) core.ProcessResult {
		if s.Args[0] == "-PDLocate" && s.Args[1] == "-stop" {
			return core.ProcessResult{Stdout: []byte("Exit Code: 0x01")}
		}
		return base.Run(ctx, s)
	}}
	r := fixtureRunner(t, "megacli", p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.deps.Waiter = &locateWaiter{cancel: cancel}
	reg, e := registry.New([]core.Module{New()}, r.deps)
	if e != nil {
		t.Fatal(e)
	}
	ex := executor.New(reg, raidClock{})
	last, emitted, e := ex.ExecuteStream(ctx, raidOp(capabilityDisks, map[string]any{"service": true}), func(v1alpha1.Result) error { return nil })
	if e != nil || !emitted || executor.ExitCode(last) == 0 || len(last.Errors) == 0 {
		t.Fatalf("cleanup failure hidden on cancel: emitted=%v err=%v last=%+v", emitted, e, last)
	}
}

func TestEventsReadTemporaryVendorFileAndLimitEntries(t *testing.T) {
	path := ""
	p := &runProcess{run: func(_ context.Context, s core.ProcessSpec) core.ProcessResult {
		if s.Args[0] == "-AdpEventLog" {
			for i, a := range s.Args {
				if a == "-f" {
					path = s.Args[i+1]
				}
			}
			if e := os.WriteFile(path, []byte("seqNum: 1\nTime: 2026-10-16 09:00:00\nDescription: Older\nseqNum: 2\nTime: 2026-10-16 10:00:00\nDescription: Command failed during historical task\n"), 0600); e != nil {
				t.Fatal(e)
			}
			return core.ProcessResult{Stdout: []byte("Exit Code: 0x00")}
		}
		return core.ProcessResult{Stdout: []byte(megaMembersFixture)}
	}}
	r := fixtureRunner(t, "megacli", p)
	out, e := r.events(context.Background(), raidOp(capabilityEvents, map[string]any{"controller": "0", "count": 1}))
	if e != nil || len(out.Errors) > 0 || len(out.Items) != 1 {
		t.Fatalf("events %+v %v", out, e)
	}
	var row eventRow
	_ = json.Unmarshal(out.Items[0].Data, &row)
	if row.Sequence == nil || *row.Sequence != 2 {
		t.Fatalf("old event selected %+v", row)
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatalf("temporary event log not cleaned: %s %v", path, e)
	}
}

func TestStorDetailedDiskHealth(t *testing.T) {
	data := []byte(`{"Controllers":[{"Command Status":{"Controller":0,"Status":"Success"},"Response Data":{"Drive /c0/e252/s0":[{"EID:Slt":"252:0","DID":8,"State":"Onln"}],"Drive /c0/e252/s0 - Detailed Information":{"Drive /c0/e252/s0 State":{"Media Error Count":2,"Other Error Count":0,"Predictive Failure Count":1,"Drive Temperature":"38C (100.4 F)"},"Drive /c0/e252/s0 Device attributes":{"SN":"SN123","Model Number":"Example"}}}}]}`)
	inv, e := parseStorCLI(data, "storcli")
	if e != nil {
		t.Fatal(e)
	}
	if len(inv.Disks) != 1 {
		t.Fatalf("disks %+v", inv.Disks)
	}
	d := inv.Disks[0]
	if d.Serial != "SN123" || d.MediaErrors == nil || *d.MediaErrors != 2 || d.PredictiveFailure == nil || !*d.PredictiveFailure || d.TemperatureC == nil {
		t.Fatalf("disk details %+v", d)
	}
}

func TestHealthReportsCacheFailureWithoutMutating(t *testing.T) {
	calls := []string{}
	p := &runProcess{run: func(_ context.Context, s core.ProcessSpec) core.ProcessResult {
		calls = append(calls, strings.Join(s.Args, " "))
		if s.Args[0] == "-AdpBbuCmd" {
			return core.ProcessResult{Stdout: []byte("Battery State: Failed\nBattery Replacement required: Yes\nExit Code: 0x00")}
		}
		return core.ProcessResult{Stdout: []byte(megaMembersFixture + "\nCurrent Cache Policy: WriteThrough\n")}
	}}
	r := fixtureRunner(t, "megacli", p)
	out, e := r.health(context.Background(), raidOp(capabilityHealth, map[string]any{"controller": "0"}))
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, item := range out.Items {
		var h healthEvidence
		_ = json.Unmarshal(item.Data, &h)
		if h.Check == "Battery State" && h.Status == "warn" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cache protection failure not shown %+v", out)
	}
	for _, call := range calls {
		if strings.Contains(call, "-PDLocate") || strings.Contains(call, "modify") || strings.Contains(call, "-Clear") {
			t.Fatalf("health mutated state: %s", call)
		}
	}
}

func TestSplitVendorFieldKeepsTransportKey(t *testing.T) {
	k, v, ok := splitVendorField("Reported Channel,Device(T:L) : 0,2(2:0)")
	if !ok || k != "Reported Channel,Device(T:L)" || v != "0,2(2:0)" {
		t.Fatalf("field %q %q %v", k, v, ok)
	}
	if progressPercent("progress -1%") != "" || progressPercent("progress 101%") != "" {
		t.Fatal("invalid progress became valid")
	}
}
