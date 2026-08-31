package system

import (
	"context"
	"net"
	"testing"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

func TestHealthReportsResourcePressure(t *testing.T) {
	files := newFakeFS()
	files.files["/proc/loadavg"] = "8.00 4.00 2.00 1/100 1\n"
	files.files["/proc/meminfo"] = "MemTotal: 100000 kB\nMemAvailable: 4000 kB\nSwapTotal: 10000 kB\nSwapFree: 5000 kB\n"
	files.stats = core.FileSystemStats{TotalBytes: 1000, AvailableBytes: 40}
	files.dirs["/sys/class/net"] = []string{}
	runner := &Runner{deps: core.Dependencies{Files: files, LogicalCPUs: 4}}
	output, err := runner.runHealth()
	if err != nil || len(output.Items) < 4 || len(output.Errors) < 2 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
}

func TestReadProcessUsesProcStatStatusAndIO(t *testing.T) {
	files := newFakeFS()
	files.files["/proc/123/stat"] = "123 (worker process) R 1 1 1 0 0 0 0 0 0 0 100 50 0 0 20 0 1 0 0 1000 20 0 0 0\n"
	files.files["/proc/123/status"] = "Name:\tworker\nUid:\t1000\t1000\t1000\t1000\n"
	files.files["/proc/123/io"] = "read_bytes: 4096\nwrite_bytes: 8192\n"
	snapshot, err := readProcess(files, 123)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.command != "worker process" || snapshot.uid != 1000 || snapshot.ticks != 150 || snapshot.readBytes != 4096 || snapshot.writeBytes != 8192 || snapshot.memory == 0 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestParseDiskHealthJSON(t *testing.T) {
	health := DiskHealth{}
	data := []byte(`{"smart_status":{"passed":true},"temperature":{"current":42},"nvme_smart_health_information_log":{"critical_warning":0,"percentage_used":7,"media_errors":2}}`)
	if err := parseSmartctlHealth(data, &health); err != nil {
		t.Fatal(err)
	}
	if health.SmartStatus != "passed" || health.TemperatureCelsius != 42 || health.PercentageUsed != 7 || health.MediaErrors != 2 {
		t.Fatalf("health=%#v", health)
	}
	critical := DiskHealth{}
	if err := parseNVMeHealth([]byte(`{"critical_warning":1,"temperature":51,"percentage_used":20,"media_errors":3}`), &critical); err != nil {
		t.Fatal(err)
	}
	if critical.SmartStatus != "critical" {
		t.Fatalf("critical=%#v", critical)
	}
}

func TestTopRejectsUnknownMode(t *testing.T) {
	runner := &Runner{deps: core.Dependencies{Files: newFakeFS()}}
	output, err := runner.runTop(context.Background(), v1alpha1.Operation{Name: "unknown"})
	if err != nil || len(output.Errors) != 1 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
}

func TestIPv4RouteSelection(t *testing.T) {
	files := newFakeFS()
	files.files["/proc/net/route"] = "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\neth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\neth0\t0000010A\t00000000\t0001\t0\t0\t0\t0000FFFF\n"
	if gateway := ipv4Gateway(files, net.ParseIP("8.8.8.8"), "eth0"); gateway != "192.168.1.1" {
		t.Fatalf("gateway=%q", gateway)
	}
	if gateway := ipv4Gateway(files, net.ParseIP("10.1.2.3"), "eth0"); gateway != "" {
		t.Fatalf("direct route gateway=%q", gateway)
	}
}

func TestConnectRejectsMissingPort(t *testing.T) {
	runner := &Runner{deps: core.Dependencies{Files: newFakeFS()}}
	output, err := runner.runNetConnect(context.Background(), v1alpha1.Operation{Name: "localhost"})
	if err != nil || len(output.Errors) != 1 || output.Errors[0].Code != v1alpha1.ErrorInvalidArgument {
		t.Fatalf("output=%#v err=%v", output, err)
	}
}
