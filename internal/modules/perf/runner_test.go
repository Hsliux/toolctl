package perf

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

type fakeTime struct{ now time.Time }

func (f *fakeTime) Now() time.Time { return f.now }
func (f *fakeTime) Wait(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		f.now = f.now.Add(duration)
		return nil
	}
}

type fakeProcesses struct {
	path   string
	result core.ProcessResult
	spec   core.ProcessSpec
}

func (p *fakeProcesses) LookPath(string) (string, error) {
	if p.path == "" {
		return "", errors.New("not found")
	}
	return p.path, nil
}

func (p *fakeProcesses) Run(_ context.Context, spec core.ProcessSpec) core.ProcessResult {
	p.spec = spec
	return p.result
}

type fakeFiles struct {
	sequences map[string][]string
	reads     map[string]int
	present   map[string]bool
}

func (f *fakeFiles) ReadFile(path string, _ int64) ([]byte, error) {
	values := f.sequences[path]
	if len(values) == 0 {
		return nil, fs.ErrNotExist
	}
	index := f.reads[path]
	if index >= len(values) {
		index = len(values) - 1
	}
	f.reads[path]++
	return []byte(values[index]), nil
}

func (*fakeFiles) ReadDir(string) ([]fs.DirEntry, error) { return nil, fs.ErrNotExist }
func (f *fakeFiles) Stat(path string) (fs.FileInfo, error) {
	if f.present[path] {
		return fakeFileInfo{}, nil
	}
	return nil, fs.ErrNotExist
}
func (*fakeFiles) StatFS(string) (core.FileSystemStats, error) {
	return core.FileSystemStats{}, fs.ErrNotExist
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "fake" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() fs.FileMode  { return fs.ModeDir }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return true }
func (fakeFileInfo) Sys() any           { return nil }

func TestLiveCPUUsesProcfsDeltasWithoutProcess(t *testing.T) {
	clock := &fakeTime{now: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
	files := &fakeFiles{sequences: map[string][]string{
		"/proc/stat": {
			"cpu 100 0 50 850 0 0 0 0 0 0\ncpu0 100 0 50 850 0 0 0 0 0 0\n",
			"cpu 120 0 60 920 0 0 0 0 0 0\ncpu0 120 0 60 920 0 0 0 0 0 0\n",
		},
	}, reads: map[string]int{}}
	runner := &Runner{deps: core.Dependencies{Files: files, Clock: clock, Waiter: clock}}
	output, err := runner.Execute(context.Background(), liveOperation(capabilityCPU))
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Errors) != 0 || len(output.Items) != 1 {
		t.Fatalf("output = %#v", output)
	}
	var stat CPUStat
	if err := json.Unmarshal(output.Items[0].Data, &stat); err != nil {
		t.Fatal(err)
	}
	if stat.UserPercent != 20 || stat.SystemPercent != 10 || stat.IdlePercent != 70 || stat.UtilPercent != 30 {
		t.Fatalf("stat = %#v", stat)
	}
}

func TestLiveCPUStreamsUntilCancellation(t *testing.T) {
	clock := &fakeTime{now: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
	files := &fakeFiles{sequences: map[string][]string{
		"/proc/stat": {
			"cpu 100 0 50 850 0 0 0 0 0 0\n",
			"cpu 120 0 60 920 0 0 0 0 0 0\n",
			"cpu 120 0 60 920 0 0 0 0 0 0\n",
			"cpu 140 0 70 990 0 0 0 0 0 0\n",
		},
	}, reads: map[string]int{}}
	runner := &Runner{deps: core.Dependencies{Files: files, Clock: clock, Waiter: clock}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	op := liveOperation(capabilityCPU)
	op.Options["live"] = json.RawMessage("true")
	emitted := 0
	err := runner.ExecuteStream(ctx, op, func(output core.RunOutput) error {
		emitted++
		if len(output.Items) != 1 || len(output.Errors) != 0 {
			t.Fatalf("output = %#v", output)
		}
		if emitted == 2 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if emitted != 2 {
		t.Fatalf("emitted = %d", emitted)
	}
}

func TestLiveCounterCalculations(t *testing.T) {
	at := time.Date(2026, 8, 6, 12, 0, 1, 0, time.UTC)
	disk := calculateDiskIOStat(
		diskCounters{},
		diskCounters{ReadIOs: 2, WriteIOs: 3, ReadSectors: 4, WriteSectors: 6, ReadMillis: 20, WriteMillis: 30, IOMillis: 500, WeightedIOMillis: 2000},
		"sda", at, 2,
	)
	if disk.ReadsPerSecond != 1 || disk.AverageRequestBytes != 1024 || disk.AverageWaitMillis != 10 || disk.AverageQueueDepth != 1 || disk.UtilizationPercent != 25 {
		t.Fatalf("disk = %#v", disk)
	}
	network := calculateNetworkStat(networkCounters{}, networkCounters{
		ReceiveBytes: 2048, TransmitBytes: 4096, ReceivePackets: 20, TransmitPackets: 40,
		ReceiveErrors: 2, TransmitErrors: 4, ReceiveDrops: 6, TransmitDrops: 8,
	}, "eth0", at, 2)
	if network.ReceiveBytesPerSec != 1024 || network.TransmitBytesPerSec != 2048 || network.ErrorsPerSecond != 3 || network.DropsPerSecond != 7 {
		t.Fatalf("network = %#v", network)
	}
}

func TestReadTCPCountersUsesHeaderNames(t *testing.T) {
	files := &fakeFiles{sequences: map[string][]string{
		"/proc/net/snmp": {"Ip: Forwarding\nIp: 1\nTcp: RtoAlgorithm MaxConn OutSegs RetransSegs\nTcp: 1 -1 200 5\n"},
	}, reads: map[string]int{}}
	counters, err := readTCPCounters(files)
	if err != nil {
		t.Fatal(err)
	}
	if counters.OutSegments != 200 || counters.RetransmittedSegments != 5 {
		t.Fatalf("counters = %#v", counters)
	}
}

func TestLiveSampleOptionsAreBounded(t *testing.T) {
	op := liveOperation(capabilityCPU)
	op.Options["interval"] = json.RawMessage("10")
	if _, err := sampleOptions(op); err == nil {
		t.Fatal("expected interval error")
	}
	op = liveOperation(capabilityCPU)
	op.Options["count"] = json.RawMessage("101")
	if _, err := sampleOptions(op); err == nil {
		t.Fatal("expected count error")
	}
}

func TestHistoryCPUIsPreservedBehindExplicitCommand(t *testing.T) {
	setTestLocation(t)
	processes := &fakeProcesses{
		path:   "/usr/bin/ssar",
		result: core.ProcessResult{ExitCode: 0, Stdout: []byte(`[{"collect_datetime":"2026-08-06T12:00:00","cpu_stat_2":"20","cpu_stat_3":"0","cpu_stat_4":"10","cpu_stat_5":"60","cpu_stat_6":"5","cpu_stat_7":"1","cpu_stat_8":"2","cpu_stat_9":"2","cpu_stat_10":"0","cpu_stat_11":"0"}]`)},
	}
	clock := &fakeTime{now: time.Now()}
	files := &fakeFiles{sequences: map[string][]string{"/proc/stat": {"cpu 1 2 3 4 5 6 7 8 9 10\n"}}, reads: map[string]int{}}
	runner := &Runner{deps: core.Dependencies{Files: files, Processes: processes, Clock: clock, Waiter: clock}}
	output, err := runner.Execute(context.Background(), historyOperation(capabilityHistoryCPU))
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Errors) != 0 || len(output.Items) != 1 {
		t.Fatalf("output = %#v", output)
	}
	if processes.spec.Path != "/usr/bin/ssar" || len(processes.spec.Args) < 4 || processes.spec.Args[0] != "-P" || processes.spec.Args[1] != "--api" || processes.spec.Args[2] != "-o" {
		t.Fatalf("process spec = %#v", processes.spec)
	}
	if !strings.Contains(processes.spec.Args[3], "alias=cpu_stat_{column}") {
		t.Fatalf("expression = %q", processes.spec.Args[3])
	}
}

func TestMissingSSAROnlyAffectsHistory(t *testing.T) {
	clock := &fakeTime{now: time.Now()}
	files := &fakeFiles{sequences: map[string][]string{"/proc/stat": {"cpu 1 2 3 4 5 6 7 8 9 10\n"}}, reads: map[string]int{}}
	runner := &Runner{deps: core.Dependencies{Files: files, Processes: &fakeProcesses{}, Clock: clock, Waiter: clock}}
	output, err := runner.Execute(context.Background(), historyOperation(capabilityHistoryCPU))
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Errors) != 1 || output.Errors[0].Code != v1alpha1.ErrorDependencyMissing {
		t.Fatalf("errors = %#v", output.Errors)
	}
}

func liveOperation(capability string) v1alpha1.Operation {
	return v1alpha1.Operation{Capability: capability, Options: map[string]json.RawMessage{
		"interval": json.RawMessage("1000"), "count": json.RawMessage("1"),
	}, TimeoutMS: 30_000}
}

func historyOperation(capability string) v1alpha1.Operation {
	return v1alpha1.Operation{Capability: capability, Options: map[string]json.RawMessage{
		"range": json.RawMessage("18000000"), "interval": json.RawMessage("300000"),
	}, TimeoutMS: 30_000}
}

func setTestLocation(t *testing.T) {
	t.Helper()
	original := time.Local
	time.Local = time.FixedZone("CST", 8*60*60)
	t.Cleanup(func() { time.Local = original })
}
