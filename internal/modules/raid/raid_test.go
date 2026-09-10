package raid

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

type fakeFS struct {
	files map[string]string
	dirs  map[string][]string
}

func newFakeFS() *fakeFS { return &fakeFS{files: map[string]string{}, dirs: map[string][]string{}} }
func (f *fakeFS) ReadFile(name string, max int64) ([]byte, error) {
	value, ok := f.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if int64(len(value)) > max {
		return nil, errors.New("too large")
	}
	return []byte(value), nil
}
func (f *fakeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	values, ok := f.dirs[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	result := []fs.DirEntry{}
	for _, value := range values {
		result = append(result, fakeDirEntry(value))
	}
	return result, nil
}
func (*fakeFS) Stat(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
func (*fakeFS) StatFS(string) (core.FileSystemStats, error) {
	return core.FileSystemStats{}, fs.ErrNotExist
}

type fakeDirEntry string

func (e fakeDirEntry) Name() string             { return string(e) }
func (fakeDirEntry) IsDir() bool                { return true }
func (fakeDirEntry) Type() fs.FileMode          { return fs.ModeDir }
func (fakeDirEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrInvalid }

type fakeProcesses struct {
	paths  map[string]string
	result core.ProcessResult
}

func (f fakeProcesses) LookPath(name string) (string, error) {
	value, ok := f.paths[name]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}
func (f fakeProcesses) Run(context.Context, core.ProcessSpec) core.ProcessResult { return f.result }

func TestDiscoverControllersMapsOEMVendor(t *testing.T) {
	files := newFakeFS()
	files.dirs["/sys/bus/pci/devices"] = []string{"0000:01:00.0", "0000:02:00.0"}
	files.files["/sys/bus/pci/devices/0000:01:00.0/class"] = "0x010400\n"
	files.files["/sys/bus/pci/devices/0000:01:00.0/vendor"] = "0x1000\n"
	files.files["/sys/bus/pci/devices/0000:01:00.0/subsystem_vendor"] = "0x1028\n"
	files.files["/sys/bus/pci/devices/0000:02:00.0/class"] = "0x010700\n"
	files.files["/sys/bus/pci/devices/0000:02:00.0/vendor"] = "0x103c\n"
	controllers, err := discoverControllers(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(controllers) != 2 || controllers[0].Backend != "perccli" || controllers[1].Backend != "ssacli" {
		t.Fatalf("controllers = %#v", controllers)
	}
}

func TestDoctorReportsInstalledUnregisteredTool(t *testing.T) {
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "perccli64")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := newFakeFS()
	files.dirs["/sys/bus/pci/devices"] = []string{"0000:01:00.0"}
	files.files["/sys/bus/pci/devices/0000:01:00.0/class"] = "0x010400"
	files.files["/sys/bus/pci/devices/0000:01:00.0/vendor"] = "0x1000"
	files.files["/sys/bus/pci/devices/0000:01:00.0/subsystem_vendor"] = "0x1028"
	runner := &Runner{deps: core.Dependencies{Files: files, Processes: fakeProcesses{paths: map[string]string{"perccli64": binary}}, Architecture: "amd64", ConfigDirectory: filepath.Join(temporary, "config")}}
	output, err := runner.Execute(context.Background(), v1alpha1.Operation{Capability: capabilityDoctor})
	if err != nil {
		t.Fatal(err)
	}
	var check DoctorCheck
	if err := json.Unmarshal(output.Items[0].Data, &check); err != nil {
		t.Fatal(err)
	}
	if check.Status != "ready" || !check.Available || check.Registered {
		t.Fatalf("check = %#v", check)
	}
	if check.Scope != "auto" || check.ConfigPath != "" {
		t.Fatalf("source = %#v", check)
	}
}

func TestDoctorReportsRegisteredToolSource(t *testing.T) {
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "storcli64")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	systemDirectory := filepath.Join(temporary, "etc-toolctl")
	if err := saveTools(systemDirectory, map[string]string{"storcli": binary}); err != nil {
		t.Fatal(err)
	}
	files := newFakeFS()
	files.dirs["/sys/bus/pci/devices"] = []string{"0000:01:00.0"}
	files.files["/sys/bus/pci/devices/0000:01:00.0/class"] = "0x010400"
	files.files["/sys/bus/pci/devices/0000:01:00.0/vendor"] = "0x1000"
	runner := &Runner{deps: core.Dependencies{Files: files, Processes: fakeProcesses{paths: map[string]string{}}, Architecture: "amd64", SystemConfigDirectory: systemDirectory}}
	output, err := runner.Execute(context.Background(), v1alpha1.Operation{Capability: capabilityDoctor})
	if err != nil {
		t.Fatal(err)
	}
	var check DoctorCheck
	if err := json.Unmarshal(output.Items[0].Data, &check); err != nil {
		t.Fatal(err)
	}
	if !check.Registered || check.Scope != "system" || check.ConfigPath != configPath(systemDirectory) {
		t.Fatalf("check = %#v", check)
	}
}

func TestInitReferencesExistingLocalTool(t *testing.T) {
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "storcli64")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{deps: core.Dependencies{Files: newFakeFS(), Processes: fakeProcesses{paths: map[string]string{}}, ConfigDirectory: filepath.Join(temporary, "config")}}
	backend, _ := json.Marshal("storcli")
	pathValue, _ := json.Marshal(binary)
	output, err := runner.Execute(context.Background(), v1alpha1.Operation{Capability: capabilityInit, Options: map[string]json.RawMessage{"backend": backend, "path": pathValue}})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Items) != 1 {
		t.Fatalf("items = %#v", output.Items)
	}
	configured := loadTools(runner.deps.ConfigDirectory)
	if configured["storcli"] != binary {
		t.Fatalf("configured = %#v", configured)
	}
	info, err := os.Stat(configPath(runner.deps.ConfigDirectory))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestParseStorCLIInventory(t *testing.T) {
	fixture := []byte(`{
  "Controllers": [{
    "Command Status": {"Controller": 0, "Status": "Success"},
    "Response Data": {
      "System Overview": [{"Ctl": 0, "Model": "MegaRAID 9560", "Hlth": "Opt"}],
      "VD LIST": [{"DG/VD": "0/0", "TYPE": "RAID1", "State": "Opt", "Size": "1.000 TB", "Name": "system"}],
      "PD LIST": [{"EID:Slt": "252:0", "DID": 10, "State": "Onln", "Intf": "SAS", "Med": "HDD", "Size": "1.818 TB", "Model": "Example Disk"}]
    }
  }]
}`)
	inventory, err := parseStorCLI(fixture, "storcli")
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Controllers) != 1 || inventory.Controllers[0].Health != HealthOptimal {
		t.Fatalf("controllers = %#v", inventory.Controllers)
	}
	if len(inventory.Volumes) != 1 || inventory.Volumes[0].RAIDLevel != "RAID1" || inventory.Volumes[0].SizeBytes == 0 {
		t.Fatalf("volumes = %#v", inventory.Volumes)
	}
	if len(inventory.Disks) != 1 || inventory.Disks[0].Enclosure != "252" || inventory.Disks[0].Slot != "0" {
		t.Fatalf("disks = %#v", inventory.Disks)
	}
}

func TestCollectMDRAID(t *testing.T) {
	files := newFakeFS()
	files.files["/proc/mdstat"] = "Personalities : [raid1]\nmd0 : active raid1 sda1[0] sdb1[1]\n      1047552 blocks super 1.2 [2/1] [U_]\nunused devices: <none>\n"
	volumes, err := collectMDRAID(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 || volumes[0].State != HealthDegraded || volumes[0].SizeBytes != 1047552*1024 {
		t.Fatalf("volumes = %#v", volumes)
	}
}

func TestSummarizeDoesNotReportOptimalForIncompleteInventory(t *testing.T) {
	inventory := newVendorInventory()
	inventory.Controllers = []Controller{{ID: "0", Backend: "storcli", Health: HealthOptimal}}
	inventory.Incomplete["storcli"] = "physical disk query failed"
	status := summarize(inventory)
	if status.Health != HealthUnknown || status.InventoryComplete || status.IncompleteBackends != "storcli" {
		t.Fatalf("status = %#v", status)
	}
}

func TestSummarizePreservesKnownFailureWhenInventoryIncomplete(t *testing.T) {
	inventory := newVendorInventory()
	inventory.Controllers = []Controller{{ID: "0", Backend: "storcli", Health: HealthFailed}}
	inventory.Incomplete["storcli"] = "physical disk query failed"
	status := summarize(inventory)
	if status.Health != HealthFailed || status.InventoryComplete {
		t.Fatalf("status = %#v", status)
	}
}

func TestParseSSACLIInventory(t *testing.T) {
	fixture := []byte(`Smart Array P408i-a SR Gen10 in Slot 0 (Embedded)
   Serial Number: CN1234
   Firmware Version: 3.00
   Controller Status: OK
   Array A
      Logical Drive: 1
         Size: 1.09 TB
         Fault Tolerance: 1
         Status: OK
      physicaldrive 1I:1:2
         Port: 1I
         Box: 1
         Bay: 2
         Status: OK
         Drive Type: Data Drive
         Interface Type: SAS
         Size: 1.2 TB
         Model: EXAMPLE-DISK
         Serial Number: DISK123
         Firmware Revision: HPD1
         Current Temperature (C): 32
`)
	inventory, err := parseSSACLI(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Controllers) != 1 || inventory.Controllers[0].Health != HealthOptimal || inventory.Controllers[0].Serial != "CN1234" {
		t.Fatalf("controllers = %#v", inventory.Controllers)
	}
	if len(inventory.Volumes) != 1 || inventory.Volumes[0].RAIDLevel != "RAID 1" || inventory.Volumes[0].SizeBytes == 0 {
		t.Fatalf("volumes = %#v", inventory.Volumes)
	}
	if len(inventory.Disks) != 1 || inventory.Disks[0].Enclosure != "1" || inventory.Disks[0].Slot != "2" || inventory.Disks[0].Serial != "DISK123" || inventory.Disks[0].TemperatureC == nil {
		t.Fatalf("disks = %#v", inventory.Disks)
	}
}

func TestParseArcconfInventory(t *testing.T) {
	fixture := []byte(`Controller information
   Controller Status                        : Optimal
   Controller Model                         : SmartRAID 3154-8i
   Controller Serial Number                 : ARC123
   Firmware                                 : 3.21

Logical device information
Logical Device number 0
   Logical device name                      : system
   RAID level                               : 1
   Status of Logical Device                 : Optimal
   Size                                     : 953344 MB

Physical Device information
Device #0
   State                                    : Online
   Reported Channel,Device(T:L)             : 0,1(1:0)
   Reported Location                        : Enclosure 2, Slot 4
   Device Type                              : Hard Drive
   Model                                    : EXAMPLE
   Serial number                            : PD123
   Firmware                                 : A001
   Total Size                               : 953869 MB
`)
	inventory, err := parseArcconf(fixture, "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Controllers) != 1 || inventory.Controllers[0].Health != HealthOptimal || inventory.Controllers[0].Serial != "ARC123" {
		t.Fatalf("controllers = %#v", inventory.Controllers)
	}
	if len(inventory.Volumes) != 1 || inventory.Volumes[0].State != HealthOptimal || inventory.Volumes[0].RAIDLevel != "RAID 1" {
		t.Fatalf("volumes = %#v", inventory.Volumes)
	}
	if len(inventory.Disks) != 1 || inventory.Disks[0].Enclosure != "2" || inventory.Disks[0].Slot != "4" || inventory.Disks[0].State != HealthOptimal || inventory.Disks[0].Serial != "PD123" {
		t.Fatalf("disks = %#v", inventory.Disks)
	}
}

func TestArcconfControllerIDsAreUniqueAndSorted(t *testing.T) {
	ids := arcconfControllerIDs("Controller 2: SmartRAID\nController 1: SmartRAID\nController 2 status\n")
	if len(ids) != 2 || ids[0] != "1" || ids[1] != "2" {
		t.Fatalf("ids = %#v", ids)
	}
}

func TestLoadToolsUsesLaterDirectoryAsOverride(t *testing.T) {
	temporary := t.TempDir()
	systemDirectory := filepath.Join(temporary, "system")
	userDirectory := filepath.Join(temporary, "user")
	if err := saveTools(systemDirectory, map[string]string{"storcli": "/system/storcli"}); err != nil {
		t.Fatal(err)
	}
	if err := saveTools(userDirectory, map[string]string{"storcli": "/user/storcli", "arcconf": "/user/arcconf"}); err != nil {
		t.Fatal(err)
	}
	configured := loadTools(systemDirectory, userDirectory)
	if configured["storcli"] != "/user/storcli" || configured["arcconf"] != "/user/arcconf" {
		t.Fatalf("configured = %#v", configured)
	}
	sources := loadToolSources(toolConfigLocation{Directory: systemDirectory, Scope: "system"}, toolConfigLocation{Directory: userDirectory, Scope: "user"})
	if sources["storcli"].Scope != "user" || sources["storcli"].ConfigPath != configPath(userDirectory) {
		t.Fatalf("sources = %#v", sources)
	}
}
