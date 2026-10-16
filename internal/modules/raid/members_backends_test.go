package raid

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

const arcMembersFixture = `Controller information
Controller Status : Optimal
Logical device information
Logical Device number 0
RAID level : 10
Logical Device segment information
Group 0, Segment 0 : Present (457862MB, SATA, SSD, Connector:0, Device:0) SERIAL-A
Group 0, Segment 1 : Present (Controller:1,Connector:0,Device:1) SERIAL-B
Group 1, Segment 0 : Missing (0,2)
Logical Device number 1
RAID level : 1
Logical device segment information
Segment 0 : Present (0,3) SERIAL-C
Physical Device information
Device #0
State : Online
Reported Channel,Device(T:L) : 0,0(0:0)
Reported Location : Enclosure 2, Slot 4
Serial number : SERIAL-A
Device #1
State : Failed
Reported Channel,Device(T:L) : 0,1(1:0)
Reported Location : Connector 0, Device 1
Serial number : SERIAL-B
Device #3
State : Online
Reported Channel,Device(T:L) : 0,3(3:0)
Serial number : SERIAL-C
Device #4
State : Hot Spare
Reported Channel,Device(T:L) : 0,4(4:0)
Serial number : SPARE
`

const megaConfigFixture = `Adapter: 0
DISK GROUP: 0
Virtual Disk: 0 (Target Id: 0)
RAID Level: Primary-1, Secondary-0, RAID Level Qualifier-0
Span Depth: 2
Enclosure Device ID: 252
Slot Number: 0
Device Id: 8
Firmware state: Online, Spun Up
Enclosure Device ID: 252
Slot Number: 1
Device Id: 9
Firmware state: Failed
DISK GROUP: 1
Virtual Drive: 1
RAID Level: Primary-0, Secondary-0, RAID Level Qualifier-0
Enclosure Device ID: 1
Slot Number: 0
Device Id: 10
Firmware state: Online, Spun Up
`

func TestArcconfMembersSegmentFormats(t *testing.T) {
	rows, e := parseArcconfMembers([]byte(arcMembersFixture), "1")
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 4 {
		t.Fatalf("members %+v", rows)
	}
	if rows[0].Slot != "4" || rows[0].Enclosure != "2" || rows[0].RAIDLevel != "RAID 10" || rows[0].Span != "0" {
		t.Fatalf("wrong location %+v", rows[0])
	}
	if rows[1].Slot != "" || rows[1].Address != "0:1" || physicalDiskStatus(rows[1].Disk) != "failed" {
		t.Fatalf("transport address became physical slot %+v", rows[1])
	}
	if physicalDiskStatus(rows[2].Disk) != "missing" || rows[2].Slot != "" {
		t.Fatalf("missing member %+v", rows[2])
	}
	if rows[3].Volume != "1" || rows[3].Serial != "SERIAL-C" {
		t.Fatal("cross-volume leakage")
	}
	bad := strings.ReplaceAll(arcMembersFixture, "Present (0,3) SERIAL-C", "Present (0,99) UNKNOWN")
	if _, e := parseArcconfMembers([]byte(bad), "1"); e == nil {
		t.Fatal("unresolved member accepted")
	}
}

func TestMegaCLILegacyHeaders(t *testing.T) {
	for _, header := range []string{"Adapter: 0", "Adapter #0", "Adapter 0 -- Virtual Drive Information:"} {
		fixture := header + "\nVirtual Disk: 3 (target id: 3)\nRAID Level: Primary-1, Secondary-0, RAID Level Qualifier-0\nEnclosure Device ID: 1\nSlot Number: 2\nDevice Id: 6\nFirmware state: Failed\n"
		rows, e := parseMegaCLIMembers([]byte(fixture))
		if e != nil {
			t.Fatal(e)
		}
		if len(rows) != 1 || rows[0].Volume != "3" {
			t.Fatalf("legacy rows %+v", rows)
		}
	}
	rows, e := parseMegaCLIConfigMembers([]byte(megaConfigFixture))
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 3 || rows[0].RAIDLevel != "RAID 10" || rows[2].Volume != "1" || rows[2].ID != "10" {
		t.Fatalf("wrong config members %+v", rows)
	}
}

func TestSSAMembersArrayIsolation(t *testing.T) {
	data := []byte(`Smart Array P408i in Slot 0
array A
logicaldrive 1 (1 TB, 1+0, OK)
physicaldrive 1I:1:1
Status: OK
physicaldrive 1I:1:2
Status: Failed
physicaldrive 1I:1:3
Drive Type: Spare Drive
array B
logicaldrive 2 (1 TB, 1, OK)
physicaldrive 1I:2:1
Status: OK
unassigned
physicaldrive 1I:2:2
Status: OK
`)
	rows, e := parseSSACLIMembers(data)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 3 || rows[0].Volume != "1" || rows[0].RAIDLevel != "RAID 10" || rows[2].Volume != "2" {
		t.Fatalf("wrong SSA rows %+v", rows)
	}
}

type memberResponses struct {
	responses map[string]string
	calls     []string
}

func (*memberResponses) LookPath(string) (string, error) { return "", os.ErrNotExist }
func (p *memberResponses) Run(_ context.Context, s core.ProcessSpec) core.ProcessResult {
	key := strings.Join(s.Args, " ")
	p.calls = append(p.calls, key)
	data, ok := p.responses[key]
	if !ok {
		return core.ProcessResult{ExitCode: 1, Stderr: []byte("unsupported query")}
	}
	return core.ProcessResult{Stdout: []byte(data)}
}

func TestMembersBackendQueries(t *testing.T) {
	for _, backend := range []string{"arcconf", "megacli"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "tool")
			if e := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0700); e != nil {
				t.Fatal(e)
			}
			if e := saveTools(dir, map[string]string{backend: binary}); e != nil {
				t.Fatal(e)
			}
			f := newFakeFS()
			f.dirs["/sys/bus/pci/devices"] = []string{"0000:01:00.0"}
			f.files["/sys/bus/pci/devices/0000:01:00.0/class"] = "0x010400"
			p := &memberResponses{responses: map[string]string{}}
			var want []string
			if backend == "arcconf" {
				f.files["/sys/bus/pci/devices/0000:01:00.0/vendor"] = "0x9005"
				p.responses["LIST nologs"] = "Controller 1: SmartRAID\nController 2: SmartRAID"
				p.responses["GETCONFIG 1 AL nologs"] = arcMembersFixture
				p.responses["GETCONFIG 2 AL nologs"] = arcMembersFixture
				want = []string{"LIST nologs", "GETCONFIG 1 AL nologs", "GETCONFIG 2 AL nologs"}
			} else {
				// Dell OEM detection previously ignored an installed MegaCLI.
				f.files["/sys/bus/pci/devices/0000:01:00.0/vendor"] = "0x1000"
				f.files["/sys/bus/pci/devices/0000:01:00.0/subsystem_vendor"] = "0x1028"
				p.responses["-LdPdInfo -aALL -NoLog"] = "Exit Code: 0x00"
				p.responses["-CfgDsply -aALL -NoLog"] = megaConfigFixture
				want = []string{"-LdPdInfo -aALL -NoLog", "-CfgDsply -aALL -NoLog"}
			}
			r := &Runner{deps: core.Dependencies{Files: f, Processes: p, ConfigDirectory: dir}}
			out, e := r.Execute(context.Background(), v1alpha1.Operation{Capability: capabilityMembers})
			if e != nil {
				t.Fatal(e)
			}
			if len(out.Errors) > 0 || len(out.Items) == 0 {
				t.Fatalf("output %+v", out)
			}
			if !reflect.DeepEqual(p.calls, want) {
				t.Fatalf("queries %v want %v", p.calls, want)
			}
		})
	}
}
