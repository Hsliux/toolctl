package system

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"testing"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

func TestCollectPhysicalHost(t *testing.T) {
	files := newFakeFS()
	files.files["/etc/os-release"] = "ID=rocky\nNAME=\"Rocky Linux\"\nVERSION_ID=\"9.4\"\nPRETTY_NAME=\"Rocky Linux 9.4\"\n"
	files.files["/proc/sys/kernel/osrelease"] = "5.14.0-test\n"
	files.files["/proc/cpuinfo"] = `processor: 0
model name: Example CPU
physical id: 0
core id: 0
flags: fpu

processor: 1
model name: Example CPU
physical id: 0
core id: 1
flags: fpu
`
	files.files["/proc/meminfo"] = "MemTotal:       67108864 kB\nMemAvailable:   50331648 kB\n"
	files.files["/sys/fs/cgroup/memory.max"] = "max\n"
	files.files["/sys/class/dmi/id/sys_vendor"] = "Dell Inc.\n"
	files.files["/sys/class/dmi/id/product_name"] = "PowerEdge R750\n"
	files.files["/sys/class/dmi/id/chassis_type"] = "23\n"
	files.files["/proc/1/cgroup"] = "0::/system.slice/init.scope\n"
	files.files["/proc/self/mountinfo"] = "29 23 8:2 / / rw,relatime - xfs /dev/sda2 rw\n"
	files.files["/proc/uptime"] = "1048320.50 0.00\n"
	files.dirs["/sys/class/block"] = []string{"loop0", "sda", "sda1", "nvme0n1"}
	files.files["/sys/class/block/loop0/size"] = "10"
	files.files["/sys/class/block/sda/size"] = "2097152\n"
	files.files["/sys/class/block/sda/device/model"] = "Example HDD\n"
	files.files["/sys/class/block/sda/queue/rotational"] = "1\n"
	files.files["/sys/class/block/sda/removable"] = "0\n"
	files.files["/sys/class/block/sda1/size"] = "1048576\n"
	files.files["/sys/class/block/sda1/partition"] = "1\n"
	files.files["/sys/class/block/sda1/queue/rotational"] = "1\n"
	files.files["/sys/class/block/sda1/removable"] = "0\n"
	files.files["/sys/class/block/nvme0n1/size"] = "4194304\n"
	files.files["/sys/class/block/nvme0n1/device/model"] = "Example NVMe\n"
	files.files["/sys/class/block/nvme0n1/queue/rotational"] = "0\n"
	files.files["/sys/class/block/nvme0n1/removable"] = "0\n"
	files.dirs["/sys/class/net"] = []string{"eth0", "lo"}
	files.files["/sys/class/net/eth0/type"] = "1\n"
	files.files["/sys/class/net/eth0/operstate"] = "up\n"
	files.files["/sys/class/net/eth0/mtu"] = "1500\n"
	files.files["/sys/class/net/eth0/speed"] = "10000\n"
	files.files["/sys/class/net/eth0/duplex"] = "full\n"
	files.files["/sys/class/net/eth0/carrier"] = "1\n"
	files.files["/sys/class/net/lo/type"] = "772\n"
	files.files["/sys/class/net/lo/operstate"] = "unknown\n"
	files.files["/sys/class/net/lo/mtu"] = "65536\n"
	files.files["/sys/class/net/lo/carrier"] = "1\n"
	files.files["/sys/devices/virtual/net/lo"] = ""
	files.stats = core.FileSystemStats{TotalBytes: 1 << 40, FreeBytes: 1 << 39, AvailableBytes: (1 << 39) - (1 << 30)}

	info, warnings := collectHost(core.Dependencies{
		Files: files, Hostname: func() (string, error) { return "node-01", nil }, Architecture: "amd64", OperatingSystem: "linux", LogicalCPUs: 2,
	})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	if info.ServerType.Type != "physical" || info.Hardware.Model != "PowerEdge R750" {
		t.Fatalf("unexpected identity: %#v %#v", info.ServerType, info.Hardware)
	}
	if info.CPU.Sockets != 1 || info.CPU.PhysicalCores != 2 || info.CPU.LogicalCPUs != 2 {
		t.Fatalf("cpu = %#v", info.CPU)
	}
	if info.Memory.TotalBytes == nil || *info.Memory.TotalBytes != 64<<30 {
		t.Fatalf("memory = %#v", info.Memory)
	}
	if info.RootFilesystem.FilesystemType != "xfs" || info.RootFilesystem.TotalBytes == nil || *info.RootFilesystem.TotalBytes != 1<<40 {
		t.Fatalf("root filesystem = %#v", info.RootFilesystem)
	}
	if len(info.BlockDevices) != 3 || info.DiskCount == nil || *info.DiskCount != 2 {
		t.Fatalf("devices = %#v, diskCount = %#v", info.BlockDevices, info.DiskCount)
	}
	if len(info.Disks) != 2 || len(info.NetworkInterfaces) != 2 || info.NICCount == nil || *info.NICCount != 1 {
		t.Fatalf("disks=%#v interfaces=%#v nicCount=%#v", info.Disks, info.NetworkInterfaces, info.NICCount)
	}
	if info.NetworkInterfaces[0].Name != "eth0" || info.NetworkInterfaces[0].SpeedMbps == nil || *info.NetworkInterfaces[0].SpeedMbps != 10000 {
		t.Fatalf("eth0 = %#v", info.NetworkInterfaces[0])
	}
	if info.UptimeSeconds == nil || *info.UptimeSeconds != 1048320.5 {
		t.Fatalf("uptime = %#v", info.UptimeSeconds)
	}
}

func TestNetworkInterfacesHandleUnknownSpeed(t *testing.T) {
	files := newFakeFS()
	files.dirs["/sys/class/net"] = []string{"ens3"}
	files.files["/sys/class/net/ens3/type"] = "1\n"
	files.files["/sys/class/net/ens3/operstate"] = "down\n"
	files.files["/sys/class/net/ens3/mtu"] = "1500\n"
	files.files["/sys/class/net/ens3/speed"] = "-1\n"
	interfaces, err := collectNetworkInterfaces(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces) != 1 || interfaces[0].SpeedMbps != nil || interfaces[0].State != "down" {
		t.Fatalf("interfaces = %#v", interfaces)
	}
}

func TestCollectCPUFallsBackToSysfsTopology(t *testing.T) {
	files := newFakeFS()
	files.files["/proc/cpuinfo"] = "processor: 0\nProcessor: ARM Neoverse\n\nprocessor: 1\nProcessor: ARM Neoverse\n"
	files.dirs["/sys/devices/system/cpu"] = []string{"cpu0", "cpu1", "cpufreq", "online"}
	for _, cpu := range []string{"cpu0", "cpu1"} {
		files.files["/sys/devices/system/cpu/"+cpu+"/topology/physical_package_id"] = "0\n"
	}
	files.files["/sys/devices/system/cpu/cpu0/topology/core_id"] = "0\n"
	files.files["/sys/devices/system/cpu/cpu1/topology/core_id"] = "1\n"
	info, _, err := collectCPU(core.Dependencies{Files: files, Architecture: "arm64", LogicalCPUs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if info.Sockets != 1 || info.PhysicalCores != 2 || info.LogicalCPUs != 2 {
		t.Fatalf("cpu = %#v", info)
	}
}

func TestRunnerListsDisksAndNICs(t *testing.T) {
	files := newFakeFS()
	files.dirs["/sys/class/block"] = []string{"sda", "sda1"}
	files.files["/sys/class/block/sda/size"] = "2097152\n"
	files.files["/sys/class/block/sda/queue/rotational"] = "1\n"
	files.files["/sys/class/block/sda/removable"] = "0\n"
	files.files["/sys/class/block/sda1/size"] = "1048576\n"
	files.files["/sys/class/block/sda1/partition"] = "1\n"
	files.dirs["/sys/class/net"] = []string{"eth0"}
	files.files["/sys/class/net/eth0/type"] = "1\n"
	files.files["/sys/class/net/eth0/operstate"] = "up\n"
	files.files["/sys/class/net/eth0/mtu"] = "1500\n"
	runner := &Runner{deps: core.Dependencies{Files: files}}
	disks, err := runner.Execute(context.Background(), v1alpha1.Operation{Capability: capabilityDiskList})
	if err != nil || len(disks.Items) != 1 || disks.Items[0].Name != "sda" {
		t.Fatalf("disks=%#v err=%v", disks, err)
	}
	nics, err := runner.Execute(context.Background(), v1alpha1.Operation{Capability: capabilityNICList})
	if err != nil || len(nics.Items) != 1 || nics.Items[0].Name != "eth0" {
		t.Fatalf("nics=%#v err=%v", nics, err)
	}
}

func TestDetectContainerWinsOverDMI(t *testing.T) {
	files := newFakeFS()
	files.files["/.dockerenv"] = ""
	result := detectServerType(files, HardwareInfo{Vendor: "Dell", Model: "PowerEdge"}, "", "linux")
	if result.Type != "container" || result.Virtualization != "docker" || result.Confidence != "high" {
		t.Fatalf("result = %#v", result)
	}
}

func TestMissingSourcesReturnNullValuesAndWarnings(t *testing.T) {
	files := newFakeFS()
	info, warnings := collectHost(core.Dependencies{
		Files: files, Hostname: func() (string, error) { return "node", nil }, Architecture: "arm64", OperatingSystem: "linux", LogicalCPUs: 4,
	})
	if len(warnings) == 0 {
		t.Fatal("expected warnings")
	}
	if info.Memory.TotalBytes != nil || info.RootFilesystem.TotalBytes != nil || info.DiskCount != nil || info.NICCount != nil || info.UptimeSeconds != nil {
		t.Fatalf("unknown values must be nil: %#v", info)
	}
}

type fakeFS struct {
	files map[string]string
	dirs  map[string][]string
	stats core.FileSystemStats
}

func newFakeFS() *fakeFS {
	return &fakeFS{files: map[string]string{}, dirs: map[string][]string{}}
}

func (f *fakeFS) ReadFile(name string, maxBytes int64) ([]byte, error) {
	value, ok := f.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if int64(len(value)) > maxBytes {
		return nil, errors.New("file too large")
	}
	return []byte(value), nil
}

func (f *fakeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	names, ok := f.dirs[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	entries := make([]fs.DirEntry, 0, len(names))
	for _, entry := range names {
		entries = append(entries, fakeDirEntry{name: entry})
	}
	return entries, nil
}

func (f *fakeFS) Stat(name string) (fs.FileInfo, error) {
	if _, ok := f.files[name]; ok {
		return fakeFileInfo{name: path.Base(name)}, nil
	}
	if _, ok := f.dirs[name]; ok {
		return fakeFileInfo{name: path.Base(name), dir: true}, nil
	}
	return nil, fs.ErrNotExist
}

func (f *fakeFS) StatFS(string) (core.FileSystemStats, error) {
	if f.stats.TotalBytes == 0 {
		return core.FileSystemStats{}, fs.ErrNotExist
	}
	return f.stats, nil
}

type fakeDirEntry struct{ name string }

func (e fakeDirEntry) Name() string               { return e.name }
func (fakeDirEntry) IsDir() bool                  { return false }
func (fakeDirEntry) Type() fs.FileMode            { return 0 }
func (e fakeDirEntry) Info() (fs.FileInfo, error) { return fakeFileInfo{name: e.name}, nil }

type fakeFileInfo struct {
	name string
	dir  bool
}

func (i fakeFileInfo) Name() string { return i.name }
func (fakeFileInfo) Size() int64    { return 0 }
func (i fakeFileInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir
	}
	return 0
}
func (i fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (i fakeFileInfo) IsDir() bool        { return i.dir }
func (fakeFileInfo) Sys() any             { return nil }
