package raid

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

const megaMembersFixture = `Adapter #0
Virtual Drive: 0 (Target Id: 0)
RAID Level : Primary-1, Secondary-0, RAID Level Qualifier-0
State : Optimal
Span Depth : 2
PD: 0 Information
Enclosure Device ID: 252
Slot Number: 0
Device Id: 10
Firmware state: Online, Spun Up
Drive's position: DiskGroup: 0, Span: 0, Arm: 0
PD: 1 Information
Enclosure Device ID: 252
Slot Number: 1
Device Id: 11
Firmware state: Failed
Drive's position: DiskGroup: 0, Span: 0, Arm: 1
PD: 2 Information
Enclosure Device ID: 252
Slot Number: 2
Device Id: 12
Firmware state: Online, Spun Up
Drive's position: DiskGroup: 0, Span: 1, Arm: 0
PD: 3 Information
Enclosure Device ID: 252
Slot Number: 3
Device Id: 13
Firmware state: Online, Spun Up
Drive's position: DiskGroup: 0, Span: 1, Arm: 1
Virtual Drive: 1 (Target Id: 1)
RAID Level : Primary-1, Secondary-0, RAID Level Qualifier-0
Span Depth : 1
Enclosure Device ID: 1
Slot Number: 0
Device Id: 14
Firmware state: Online, Spun Up
Adapter #1
Virtual Drive: 0 (Target Id: 0)
RAID Level : Primary-0, Secondary-0, RAID Level Qualifier-0
Enclosure Device ID: 1
Slot Number: 0
Device Id: 0
Firmware state: Online, Spun Up
`

func TestMegaMembersKeepVolumeAndControllerBoundaries(t *testing.T) {
	rows, err := parseMegaCLIMembers([]byte(megaMembersFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("rows %d", len(rows))
	}
	for i := 0; i < 4; i++ {
		if rows[i].Volume != "0" || rows[i].Controller != "0" || rows[i].RAIDLevel != "RAID 10" {
			t.Fatalf("wrong RAID10 member %+v", rows[i])
		}
	}
	if rows[2].Span != "1" || physicalDiskStatus(rows[1].Disk) != "failed" {
		t.Fatal("span or failure missing")
	}
	if rows[4].Volume != "1" || rows[4].RAIDLevel != "RAID 1" || rows[5].Controller != "1" {
		t.Fatal("cross-volume/controller leakage")
	}
	if _, err := parseMegaCLIMembers([]byte("Adapter #0\nVirtual Drive: 0\nRAID Level: Primary-1\n")); err == nil {
		t.Fatal("missing membership accepted")
	}
}

func TestStorMembersJoinDiskGroups(t *testing.T) {
	data := []byte(`{"Controllers":[{"Command Status":{"Controller":0,"Status":"Success"},"Response Data":{"VD LIST":[{"DG/VD":"1/0","TYPE":"RAID10"},{"DG/VD":"2/1","TYPE":"RAID1"}],"PD LIST":[{"DG":1,"DID":8,"EID:Slt":"252:0","State":"Onln"},{"DG":2,"DID":9,"EID:Slt":"252:1","State":"Failed"},{"DG":"-","DID":10,"EID:Slt":"252:2","State":"UGood"}]}}]}`)
	rows, err := parseStorCLIMembers(data, "storcli")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Volume != "0" || rows[0].ID != "8" || rows[1].Volume != "1" || rows[1].ID != "9" {
		t.Fatalf("wrong groups %+v", rows)
	}
	if _, err := parseStorCLIMembers([]byte(`{"Controllers":[{"Command Status":{"Controller":0,"Status":"Failure"}}]}`), "storcli"); err == nil {
		t.Fatal("vendor failure ignored")
	}
}

func TestMDMembers(t *testing.T) {
	f := newFakeFS()
	f.files["/proc/mdstat"] = "md0 : active raid1 sda1[0] sdb1[1](F) sdc1[2](S)\n"
	rows := collectMDMembers(f)
	if len(rows) != 3 || rows[1].Status != "failed" || rows[2].Status != "spare" || rows[0].Volume != "md0" {
		t.Fatalf("members %+v", rows)
	}
}

func TestMembersCommandUsesRegisteredMegaCLI(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "MegaCli64")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveTools(dir, map[string]string{"megacli": binary}); err != nil {
		t.Fatal(err)
	}
	f := newFakeFS()
	f.dirs["/sys/bus/pci/devices"] = []string{"0000:01:00.0"}
	f.files["/sys/bus/pci/devices/0000:01:00.0/class"] = "0x010400"
	f.files["/sys/bus/pci/devices/0000:01:00.0/vendor"] = "0x1000"
	r := &Runner{deps: core.Dependencies{Files: f, Processes: fakeProcesses{result: core.ProcessResult{Stdout: []byte(megaMembersFixture)}}, ConfigDirectory: dir}}
	out, err := r.Execute(context.Background(), v1alpha1.Operation{Capability: capabilityMembers})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Errors) > 0 || len(out.Items) != 6 {
		t.Fatalf("output %+v", out)
	}
	var row map[string]any
	if err := json.Unmarshal(out.Items[0].Data, &row); err != nil {
		t.Fatal(err)
	}
	if row["controller"] != "0" || row["volume"] != "0" || row["megacliArgs"] != "-physdrv '[252:0]' -a0" {
		t.Fatalf("JSON %+v", row)
	}
}
