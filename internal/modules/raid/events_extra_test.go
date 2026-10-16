package raid

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	"toolctl/internal/executor"
)

type kernelProcess struct {
	runProcess
	paths map[string]string
	calls []core.ProcessSpec
}

func (p *kernelProcess) LookPath(name string) (string, error) {
	if path := p.paths[name]; path != "" {
		return path, nil
	}
	return "", os.ErrNotExist
}
func (p *kernelProcess) Run(ctx context.Context, s core.ProcessSpec) core.ProcessResult {
	p.calls = append(p.calls, s)
	return p.run(ctx, s)
}

func TestSSAEventDiagnosticArchive(t *testing.T) {
	archivePath := ""
	p := &runProcess{run: func(_ context.Context, s core.ProcessSpec) core.ProcessResult {
		for _, arg := range s.Args {
			if strings.HasPrefix(arg, "file=") {
				archivePath = strings.TrimPrefix(arg, "file=")
			}
		}
		f, e := os.Create(archivePath)
		if e != nil {
			t.Fatal(e)
		}
		w := zip.NewWriter(f)
		for name, data := range map[string]string{
			"logs/controller-events.json": `{"events":[{"SequenceNumber":"2","Timestamp":"2026-10-16T08:00:00Z","Severity":"Warning","Description":"Drive lost"}]}`,
			"logs/events.xml":             `<Events><Event sequence="3"><Time>2026-10-16T09:00:00Z</Time><Severity>Info</Severity><Message>Rebuild started</Message></Event></Events>`,
			"../../escape.txt":            "should not be extracted",
		} {
			entry, e := w.Create(name)
			if e != nil {
				t.Fatal(e)
			}
			if _, e := entry.Write([]byte(data)); e != nil {
				t.Fatal(e)
			}
		}
		if e := w.Close(); e != nil {
			t.Fatal(e)
		}
		_ = f.Close()
		return core.ProcessResult{Stdout: []byte("Report generated successfully")}
	}}
	r := fixtureRunner(t, "ssacli", p)
	binary, _ := discoverTool(p, "ssacli", loadTools(r.deps.ConfigDirectory))
	rows, e := r.ssaDiagnosticEvents(context.Background(), Controller{ID: "0", Backend: "ssacli"}, binary)
	if e != nil || len(rows) != 2 {
		t.Fatalf("events %+v error=%v", rows, e)
	}
	for _, row := range rows {
		if row.Source != "ssacli-diagnostic" {
			t.Fatal("missing source")
		}
	}
	if _, e := os.Stat(archivePath); !os.IsNotExist(e) {
		t.Fatal("diagnostic archive not cleaned")
	}
}

func TestKernelEventFallbackMatchesController(t *testing.T) {
	p := &kernelProcess{paths: map[string]string{"journalctl": "/fake/journalctl"}}
	p.run = func(_ context.Context, s core.ProcessSpec) core.ProcessResult {
		if s.Path == "/fake/journalctl" {
			return core.ProcessResult{Stdout: []byte(`{"MESSAGE":"hpsa 0000:03:00.0: drive failed","__REALTIME_TIMESTAMP":"1792137600000000","PRIORITY":"3"}` + "\n" + `{"MESSAGE":"hpsa 0000:04:00.0: unrelated drive","__REALTIME_TIMESTAMP":"1792137600000001","PRIORITY":"4"}`)}
		}
		return core.ProcessResult{ExitCode: 1, Stderr: []byte("diagnostic export unavailable")}
	}
	r := fixtureRunner(t, "ssacli", p)
	binary, _ := discoverTool(p, "ssacli", loadTools(r.deps.ConfigDirectory))
	c := Controller{ID: "0", Backend: "ssacli", PCIAddress: "0000:03:00.0"}
	inv := newVendorInventory()
	inv.Controllers = []Controller{c}
	rows, notes, e := r.additionalEvents(context.Background(), c, binary, inv)
	if e != nil || len(rows) != 1 || rows[0].Source != "kernel-journal" || rows[0].Severity != "error" || len(notes) != 2 {
		t.Fatalf("rows %+v notes %+v err %v", rows, notes, e)
	}
	for _, call := range p.calls {
		for _, arg := range call.Args {
			if arg == "--clear" || arg == "-c" || arg == "--read-clear" {
				t.Fatal("log clearing option used")
			}
		}
	}
}

func TestKernelPermissionFailureAndMultipleControllerAmbiguity(t *testing.T) {
	p := &kernelProcess{paths: map[string]string{"journalctl": "/fake/journalctl", "dmesg": "/fake/dmesg"}}
	p.run = func(context.Context, core.ProcessSpec) core.ProcessResult {
		return core.ProcessResult{ExitCode: 1, Stderr: []byte("permission denied")}
	}
	r := fixtureRunner(t, "ssacli", p)
	c := Controller{ID: "0", Backend: "ssacli"}
	inv := newVendorInventory()
	inv.Controllers = []Controller{c}
	if _, _, e := r.kernelEvents(context.Background(), c, inv); e == nil {
		t.Fatal("unreadable logs treated as empty success")
	}
	inv.Controllers = append(inv.Controllers, Controller{ID: "1", Backend: "ssacli"})
	binary, _ := discoverTool(p, "ssacli", loadTools(r.deps.ConfigDirectory))
	if _, _, e := r.additionalEvents(context.Background(), c, binary, inv); e == nil {
		t.Fatal("ambiguous PCI association accepted")
	}
}

func TestMDEventsAndDmesgOrdering(t *testing.T) {
	p := &kernelProcess{paths: map[string]string{"dmesg": "/fake/dmesg"}}
	p.run = func(_ context.Context, s core.ProcessSpec) core.ProcessResult {
		return core.ProcessResult{Stdout: []byte("[1.000] md/raid1:md0: started\n[2.000] md/raid1:md01: unrelated\n[3.000] md/raid1:md0: disk failed\n")}
	}
	r := fixtureRunner(t, "megacli", p)
	inv := newVendorInventory()
	inv.Volumes = []Volume{{Backend: "mdraid", ID: "md0"}}
	rows, source, e := r.kernelEvents(context.Background(), Controller{ID: "mdraid", Backend: "mdraid"}, inv)
	if e != nil || source != "dmesg" || len(rows) != 2 || !strings.Contains(rows[0].Message, "disk failed") || rows[0].Time != "boot+3.000s" {
		t.Fatalf("rows %+v source %s err %v", rows, source, e)
	}
}

func TestRemainingQueryStatusesAndDetection(t *testing.T) {
	if _, b := classifyPCI("0x1000", "0x103c"); b != "storcli" {
		t.Fatal("HPE MR selected SSA CLI")
	}
	if _, b := classifyPCI("0x9005", "0x103c"); b != "ssacli" {
		t.Fatal("HPE SR selected wrong backend")
	}
	rows := replaceBackendControllers([]Controller{{Backend: "ssacli", PCIAddress: "0000:01:00.0"}, {Backend: "ssacli", PCIAddress: "0000:02:00.0"}}, "ssacli", []Controller{{ID: "1", Backend: "ssacli"}, {ID: "0", Backend: "ssacli"}})
	if rows[0].PCIAddress != "" || rows[1].PCIAddress != "" {
		t.Fatal("PCI mapping inferred from array order")
	}
	inv := newVendorInventory()
	inv.Incomplete["storcli"] = "query failed"
	out := core.RunOutput{}
	completeQuery(&out, inv, selection{})
	if len(out.Errors) == 0 {
		t.Fatal("source failure hidden")
	}
	result := v1alpha1.Result{Status: v1alpha1.StatusFailure, Errors: out.Errors}
	if executor.ExitCode(result) == 0 {
		t.Fatal("source error returned success")
	}
	inv = newVendorInventory()
	inv.Controllers = []Controller{{Backend: "megacli", ID: "0"}}
	out = core.RunOutput{}
	completeQuery(&out, inv, selection{controller: "9"})
	if len(out.Errors) != 1 || out.Errors[0].Code != v1alpha1.ErrorInvalidArgument {
		t.Fatalf("unknown controller %+v", out)
	}
	cache := []byte("logicaldrive 1 (1 TB, RAID 1, OK)\nCache Ratio: 10% Read / 90% Write\n")
	tasks, e := parseTextTasks(cache, "0", "ssacli", "background")
	if e != nil || len(tasks) != 1 || tasks[0].Status != "not-reported" {
		t.Fatalf("cache ratio became task progress %+v %v", tasks, e)
	}
}

func TestSSAEventsCommandUsesNativeDiagnostic(t *testing.T) {
	p := &runProcess{run: func(_ context.Context, s core.ProcessSpec) core.ProcessResult {
		if len(s.Args) > 2 && s.Args[2] == "diag" {
			path := ""
			for _, arg := range s.Args {
				if strings.HasPrefix(arg, "file=") {
					path = strings.TrimPrefix(arg, "file=")
				}
			}
			f, e := os.Create(path)
			if e != nil {
				t.Fatal(e)
			}
			w := zip.NewWriter(f)
			entry, e := w.Create("events.json")
			if e != nil {
				t.Fatal(e)
			}
			_, e = entry.Write([]byte(`{"events":[{"Timestamp":"2026-10-16T10:00:00Z","Description":"Physical drive failure","Severity":"Critical"}]}`))
			if e != nil {
				t.Fatal(e)
			}
			_ = w.Close()
			_ = f.Close()
			return core.ProcessResult{Stdout: []byte("Diagnostic report generated")}
		}
		return core.ProcessResult{Stdout: []byte("Smart Array P408i in Slot 0\nController Status: OK\n")}
	}}
	r := fixtureRunner(t, "ssacli", p)
	out, e := r.Execute(context.Background(), raidOp(capabilityEvents, map[string]any{"backend": "ssacli", "controller": "0"}))
	if e != nil || len(out.Errors) > 0 || len(out.Items) != 1 {
		t.Fatalf("SSA events %+v %v", out, e)
	}
	var row eventRow
	_ = json.Unmarshal(out.Items[0].Data, &row)
	if row.Source != "ssacli-diagnostic" {
		t.Fatalf("source %+v", row)
	}
}

func TestMDEventsCommandUsesKernelSource(t *testing.T) {
	p := &kernelProcess{paths: map[string]string{"dmesg": "/fake/dmesg"}}
	p.run = func(context.Context, core.ProcessSpec) core.ProcessResult {
		return core.ProcessResult{Stdout: []byte("[1.0] md/raid1:md0: array started\n[2.0] md/raid1:md0: disk failed\n")}
	}
	f := newFakeFS()
	f.dirs["/sys/bus/pci/devices"] = []string{}
	f.files["/proc/mdstat"] = "md0 : active raid1 sda1[0] sdb1[1](F)\n 1024 blocks [2/1] [U_]\n"
	r := &Runner{deps: core.Dependencies{Files: f, Processes: p}}
	out, e := r.Execute(context.Background(), raidOp(capabilityEvents, map[string]any{"backend": "mdraid", "count": 1}))
	if e != nil || len(out.Errors) > 0 || len(out.Items) != 1 {
		t.Fatalf("MD events %+v %v", out, e)
	}
	var row eventRow
	_ = json.Unmarshal(out.Items[0].Data, &row)
	if row.Source != "kernel-dmesg" || !strings.Contains(row.Message, "disk failed") {
		t.Fatalf("source %+v", row)
	}
}

func TestArcconfMemberPartialResults(t *testing.T) {
	p := &memberResponses{responses: map[string]string{"LIST nologs": "Controller 1: SmartRAID\nController 2: SmartRAID", "GETCONFIG 1 AL nologs": arcMembersFixture}}
	r := fixtureRunner(t, "arcconf", p)
	out, e := r.Execute(context.Background(), raidOp(capabilityMembers, nil))
	if e != nil || len(out.Items) != 4 || len(out.Errors) != 1 {
		t.Fatalf("partial members %+v %v", out, e)
	}
}
