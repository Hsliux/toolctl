package raid

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

func raidOp(capability string, options map[string]any) v1alpha1.Operation {
	op := v1alpha1.Operation{Capability: capability, Options: map[string]json.RawMessage{}}
	for k, v := range options {
		b, _ := json.Marshal(v)
		op.Options[k] = b
	}
	return op
}
func fixtureRunner(t *testing.T, backend string, p core.ProcessExecutor) *Runner {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, backend)
	if e := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := saveTools(dir, map[string]string{backend: binary}); e != nil {
		t.Fatal(e)
	}
	f := newFakeFS()
	f.dirs["/sys/bus/pci/devices"] = []string{"0000:01:00.0"}
	f.files["/sys/bus/pci/devices/0000:01:00.0/class"] = "0x010400"
	vendor := "0x1000"
	if backend == "arcconf" {
		vendor = "0x9005"
	}
	if backend == "ssacli" {
		vendor = "0x103c"
	}
	f.files["/sys/bus/pci/devices/0000:01:00.0/vendor"] = vendor
	return &Runner{deps: core.Dependencies{Files: f, Processes: p, ConfigDirectory: dir, Waiter: &locateWaiter{}, Hostname: func() (string, error) { return "server-a", nil }}}
}
func TestMegaVolumesUseCompoundLevel(t *testing.T) {
	inv := newVendorInventory()
	e := parseMegaCLIVolumes([]byte("Adapter #0\nVirtual Drive: 0\nRAID Level: Primary-1, Secondary-0\nSpan Depth: 2\nAdapter #1\nVirtual Disk: 1\nRAID Level: Primary-5, Secondary-0\nSpan Depth: 2\n"), &inv)
	if e != nil || len(inv.Volumes) != 2 || inv.Volumes[0].RAIDLevel != "RAID 10" || inv.Volumes[1].RAIDLevel != "RAID 50" {
		t.Fatalf("volumes %+v err=%v", inv.Volumes, e)
	}
}
func TestMembersSelectionBeforeOutput(t *testing.T) {
	p := &memberResponses{responses: map[string]string{"-LdPdInfo -aALL -NoLog": megaMembersFixture}}
	r := fixtureRunner(t, "megacli", p)
	out, e := r.Execute(context.Background(), raidOp(capabilityMembers, map[string]any{"controller": "0", "volume": "0", "abnormal": true}))
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Items) != 1 {
		t.Fatalf("expected one failed member, %+v", out)
	}
	var m Member
	_ = json.Unmarshal(out.Items[0].Data, &m)
	if m.Volume != "0" || m.Slot != "1" || m.Arm != "1" || m.DiskGroup != "0" {
		t.Fatalf("member %+v", m)
	}
}

func TestStorTopologyPreservesMissingMirrorMember(t *testing.T) {
	rows, e := parseStorCLIMembers([]byte(`{"Controllers":[{"Command Status":{"Controller":0,"Status":"Success"},"Response Data":{"VD LIST":[{"DG/VD":"0/0","TYPE":"RAID10"}],"PD LIST":[{"DG":0,"DID":8,"EID:Slt":"252:0","State":"Onln"}],"TOPOLOGY":[{"DG":0,"Arr":0,"Row":0,"DID":8,"EID:Slot":"252:0","State":"Onln"},{"DG":0,"Arr":0,"Row":1,"DID":"-","EID:Slot":"-","State":"Msng"}]}}]}`), "storcli")
	if e != nil || len(rows) != 2 {
		t.Fatalf("members %+v %v", rows, e)
	}
	if rows[0].Span != "0" || rows[0].Arm != "0" || rows[1].Arm != "1" || rows[1].Slot != "" || physicalDiskStatus(rows[1].Disk) != "missing" {
		t.Fatalf("topology lost or invented slot: %+v", rows)
	}
}

func TestInventoryMergesDetailsAndUnknownHealth(t *testing.T) {
	n := uint64(4)
	old := Disk{ID: "8", VendorState: "Onln", State: HealthOptimal, Model: "Example"}
	merged := mergeDiskFields(old, Disk{MediaErrors: &n, Serial: "SN1"})
	if merged.Model != "Example" || merged.Serial != "SN1" || merged.MediaErrors == nil || merged.State != HealthOptimal {
		t.Fatalf("details lost %+v", merged)
	}
	inv := newVendorInventory()
	inv.Controllers = []Controller{{Health: HealthUnknown}}
	inv.Volumes = []Volume{{State: HealthOptimal}}
	if summarize(inv).Health != HealthUnknown {
		t.Fatal("unknown controller health became optimal")
	}
	if abnormalDisk(Disk{Status: "online"}) || abnormalDisk(Disk{Status: "spare"}) {
		t.Fatal("MD online/spare selected as abnormal")
	}
}
func TestServiceIncludesMissingAndEnrichedHandoff(t *testing.T) {
	p := &memberResponses{responses: map[string]string{
		"LIST nologs":                        "Controller 1: SmartRAID",
		"GETCONFIG 1 nologs":                 arcMembersFixture,
		"GETCONFIG 1 AL nologs":              arcMembersFixture,
		"IDENTIFY 1 DEVICE 0 1 START nologs": "Command completed successfully.",
		"IDENTIFY 1 DEVICE 0 1 STOP nologs":  "Command completed successfully.",
	}}
	r := fixtureRunner(t, "arcconf", p)
	snapshots := []core.RunOutput{}
	e := r.ExecuteStream(context.Background(), raidOp(capabilityDisks, map[string]any{"service": true, "volume": "0"}), func(out core.RunOutput) error { snapshots = append(snapshots, out); return nil })
	if e != nil {
		t.Fatalf("%v calls=%v", e, p.calls)
	}
	if len(snapshots) != 2 || len(snapshots[0].Items) != 2 {
		t.Fatalf("snapshots %+v", snapshots)
	}
	foundMissing := false
	for _, item := range snapshots[0].Items {
		var d Disk
		_ = json.Unmarshal(item.Data, &d)
		if len(d.Volumes) != 1 || d.Volumes[0] != "0" || !strings.Contains(d.Handoff, "RAID 10") {
			t.Fatalf("handoff %+v", d)
		}
		if d.Status == "missing" {
			foundMissing = true
			if d.Locate != "unavailable-missing" || d.Slot != "" {
				t.Fatalf("missing %+v", d)
			}
		}
	}
	if !foundMissing {
		t.Fatal("missing disk disappeared from service output")
	}
	for _, call := range p.calls {
		if strings.HasPrefix(call, "IDENTIFY") && strings.Contains(call, "DEVICE 0 2") {
			t.Fatal("missing disk was located")
		}
	}
}
func TestLocateArgumentsAndTargetValidation(t *testing.T) {
	cases := []struct {
		d    Disk
		want []string
	}{
		{Disk{Backend: "arcconf", Controller: "1", ID: "0:2"}, []string{"IDENTIFY", "1", "DEVICE", "0", "2", "START", "nologs"}},
		{Disk{Backend: "ssacli", Controller: "0", ID: "1I:1:2"}, []string{"ctrl", "slot=0", "pd", "1I:1:2", "modify", "led=on"}},
	}
	for _, tc := range cases {
		args, e := locateArgs(tc.d, "start")
		if e != nil || !reflect.DeepEqual(args, tc.want) {
			t.Fatalf("args %v, err %v", args, e)
		}
	}
	for _, d := range []Disk{{Backend: "arcconf", Controller: "ALL", ID: "0:1"}, {Backend: "ssacli", Controller: "0", ID: "all"}, {Backend: "storcli", Controller: "0", Enclosure: "all", Slot: "1"}} {
		if _, e := locateArgs(d, "start"); e == nil {
			t.Fatal("wildcard accepted")
		}
	}
	p := &memberResponses{}
	r := &Runner{deps: core.Dependencies{Processes: p}}
	e := r.ExecuteStream(context.Background(), raidOp(capabilityLocate, map[string]any{"slot": "2", "enclosure": "1"}), func(core.RunOutput) error { return nil })
	if e == nil || len(p.calls) > 0 {
		t.Fatal("incomplete target performed I/O")
	}
}
func TestExplicitLocateStopDoesNotWait(t *testing.T) {
	p := &memberResponses{responses: map[string]string{"-PDLocate -stop -physdrv[1:2] -a0 -NoLog": "Exit Code: 0x00"}}
	r := fixtureRunner(t, "megacli", p)
	r.deps.Files = newFakeFS()
	waiter := &locateWaiter{}
	r.deps.Waiter = waiter
	var final Disk
	e := r.ExecuteStream(context.Background(), raidOp(capabilityLocate, map[string]any{"controller": "0", "enclosure": "1", "slot": "2", "backend": "megacli", "stop": true}), func(out core.RunOutput) error {
		if len(out.Items) > 0 {
			_ = json.Unmarshal(out.Items[0].Data, &final)
		}
		return nil
	})
	if e != nil || final.Locate != "off" || waiter.duration != 0 {
		t.Fatalf("stop %v %+v wait=%v", e, final, waiter.duration)
	}
}
func TestTaskParsersKeepRealProgress(t *testing.T) {
	rows, e := parseTextTasks([]byte("Task type : Rebuild\nLogical device : 0\nPercentage complete : 37\n"), "1", "arcconf", "background")
	if e != nil || len(rows) != 1 || rows[0].Progress != "37%" || rows[0].Target != "0" {
		t.Fatalf("tasks %+v %v", rows, e)
	}
	rows, e = parseTextTasks([]byte("Virtual Drive: 0\nOperation not in progress\nVirtual Drive: 1\nCompleted 18%\n"), "0", "megacli", "initialization")
	if e != nil || len(rows) != 1 || rows[0].Target != "volume 1" || rows[0].Progress != "18%" {
		t.Fatalf("mixed task output %+v %v", rows, e)
	}
	if _, e := parseTextTasks([]byte("unexpected output"), "0", "megacli", "rebuild"); e == nil {
		t.Fatal("unrecognized output treated as idle")
	}
	rows, e = parseJSONTasks([]byte(`{"Controllers":[{"Command Status":{"Status":"Success"},"Response Data":{"VD Operation Status":[{"VD":0,"Progress%":48,"Status":"In progress"}]}}]}`), "0", "storcli", "init")
	if e != nil || rows[0].Progress != "48%" {
		t.Fatalf("JSON tasks %+v %v", rows, e)
	}
}

func TestLiveTasksEmitBeforeWaiting(t *testing.T) {
	p := &memberResponses{responses: map[string]string{"LIST nologs": "Controller 1: SmartRAID", "GETCONFIG 1 nologs": arcMembersFixture, "GETSTATUS 1 nologs": "Task type: Rebuild\nLogical device: 0\nPercentage complete: 37\n"}}
	r := fixtureRunner(t, "arcconf", p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &locateWaiter{cancel: cancel}
	r.deps.Waiter = w
	n := 0
	e := r.ExecuteStream(ctx, raidOp(capabilityTasks, map[string]any{"live": true, "interval": 1000}), func(out core.RunOutput) error {
		n++
		if w.duration != 0 || len(out.Items) != 1 || len(out.Errors) > 0 {
			t.Fatalf("bad first task snapshot %+v", out)
		}
		return nil
	})
	if e != nil || n != 1 || w.duration != time.Second {
		t.Fatalf("stream count %d wait %v error %v", n, w.duration, e)
	}
}
func TestHealthParsersPreserveUnknownAndCounters(t *testing.T) {
	inv := newVendorInventory()
	e := parseMegaCLIDisks([]byte("Adapter #0\nEnclosure Device ID: 1\nSlot Number: 2\nMedia Error Count: 3\nOther Error Count: 0\nPredictive Failure Count: 1\nDrive Temperature: 35C\n"), &inv)
	if e != nil {
		t.Fatal(e)
	}
	d := inv.Disks[0]
	if d.MediaErrors == nil || *d.MediaErrors != 3 || d.PredictiveFailure == nil || !*d.PredictiveFailure || d.TemperatureC == nil {
		t.Fatalf("health fields %+v", d)
	}
	if vendorUint("not supported") != nil {
		t.Fatal("unknown became zero")
	}
	fields := parseHealthFields([]byte("Virtual Drive: 1\nCurrent Cache Policy: WriteThrough\nBattery Replacement required: Yes\n"), "megacli")
	if len(fields) != 2 || fields[0].target != "volume 1" || evidenceStatus(fields[1].key, fields[1].value) != "warn" {
		t.Fatalf("evidence %+v", fields)
	}
}
func TestEventEntriesAreWholeRecords(t *testing.T) {
	rows := parseEvents([]byte("seqNum: 0x01\nTime: 2026-10-16 09:00:00\nClass: Critical\nDescription: Disk failed\nseqNum: 0x02\nTime: 2026-10-16 10:00:00\nClass: Info\nDescription: Rebuild started\n"), "0", "megacli")
	if len(rows) != 2 || *rows[1].Sequence != 2 || rows[1].Message != "Rebuild started" || rows[0].Severity != "Critical" {
		t.Fatalf("events %+v", rows)
	}
	if !eventStamp(rows[1].Time).After(eventStamp(rows[0].Time)) {
		t.Fatal("event ordering")
	}
	if eventStamp("unrecognized").IsZero() == false {
		t.Fatal("invented event date")
	}
	for _, op := range []v1alpha1.Operation{raidOp(capabilityTasks, map[string]any{"interval": 0}), raidOp(capabilityEvents, map[string]any{"count": 0}), raidOp(capabilityLocate, map[string]any{"controller": "0", "enclosure": "1", "slot": "2", "duration": int64(time.Hour/time.Millisecond) * 25})} {
		r := &Runner{}
		var e error
		if op.Capability == capabilityEvents {
			_, e = r.events(context.Background(), op)
		} else {
			e = r.ExecuteStream(context.Background(), op, func(core.RunOutput) error { return nil })
		}
		if e == nil {
			t.Fatalf("bad options accepted %+v", op)
		}
	}
}
