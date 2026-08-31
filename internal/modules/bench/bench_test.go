package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

const fioFixture = `{
  "fio version": "fio-3.38",
  "jobs": [{
    "jobname": "toolctl-rand-write", "error": 0,
    "read": {"io_bytes": 0, "bw_bytes": 0, "iops": 0, "total_ios": 0},
    "write": {
      "io_bytes": 1073741824, "bw_bytes": 104857600, "iops": 25600.5, "total_ios": 256005,
      "clat_ns": {"mean": 320000, "max": 4100000, "percentile": {"50.000000": 280000, "95.000000": 520000, "99.000000": 810000, "99.900000": 1500000}}
    },
    "usr_cpu": 3.2, "sys_cpu": 12.5, "ctx": 1234
  }],
  "disk_util": [{"name": "sda", "util": 97.5}]
}`

type fakeFiles struct {
	stats core.FileSystemStats
	files map[string]string
}

func (f fakeFiles) ReadFile(name string, _ int64) ([]byte, error) {
	if value, ok := f.files[name]; ok {
		return []byte(value), nil
	}
	return nil, fs.ErrNotExist
}
func (fakeFiles) ReadDir(string) ([]fs.DirEntry, error)         { return nil, fs.ErrNotExist }
func (fakeFiles) Stat(string) (fs.FileInfo, error)              { return nil, fs.ErrNotExist }
func (f fakeFiles) StatFS(string) (core.FileSystemStats, error) { return f.stats, nil }

type fakeProcesses struct {
	mu    sync.Mutex
	specs []core.ProcessSpec
}

func (*fakeProcesses) LookPath(name string) (string, error) {
	if name != "fio" {
		return "", errors.New("not found")
	}
	return "/usr/bin/fio", nil
}
func (f *fakeProcesses) Run(_ context.Context, spec core.ProcessSpec) core.ProcessResult {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	if len(spec.Args) == 1 && spec.Args[0] == "--version" {
		return core.ProcessResult{ExitCode: 0, Stdout: []byte("fio-3.38\n")}
	}
	return core.ProcessResult{ExitCode: 0, Stdout: []byte(fioFixture), Duration: 10 * time.Second}
}

func defaultOperation(directory string) v1alpha1.Operation {
	return v1alpha1.Operation{ID: "op-test", Capability: capabilityDiskRun, Name: directory, Options: map[string]json.RawMessage{
		"profile": json.RawMessage(`"rand-write"`), "size": json.RawMessage(`"1G"`), "duration": json.RawMessage(`10000`),
		"warmup": json.RawMessage(`2000`), "jobs": json.RawMessage(`1`), "depth": json.RawMessage(`16`),
		"block-size": json.RawMessage(`"auto"`), "keep-file": json.RawMessage(`false`),
		"dry-run": json.RawMessage(`false`), "force": json.RawMessage(`false`),
	}}
}

func TestDiskBenchmarkDryRunDoesNotCreateFileOrRunFIO(t *testing.T) {
	directory := t.TempDir()
	processes := &fakeProcesses{}
	op := defaultOperation(directory)
	op.Options["dry-run"] = json.RawMessage(`true`)
	runner := &Runner{deps: core.Dependencies{Files: fakeFiles{stats: core.FileSystemStats{AvailableBytes: 10 << 30}, files: map[string]string{"/proc/self/mountinfo": "29 23 8:2 / / rw - xfs /dev/sda2 rw\n"}}, Processes: processes, OperatingSystem: "linux"}}
	output, err := runner.Execute(context.Background(), op)
	if err != nil || len(output.Errors) != 0 || len(output.Items) != 1 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	var plan DiskBenchmarkPlan
	if err := json.Unmarshal(output.Items[0].Data, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "dry-run" || !plan.RootFilesystem || plan.Mountpoint != "/" || len(plan.Arguments) == 0 {
		t.Fatalf("plan = %#v", plan)
	}
	if len(processes.specs) != 0 {
		t.Fatalf("fio unexpectedly executed: %#v", processes.specs)
	}
	if _, err := os.Stat(plan.File); !os.IsNotExist(err) {
		t.Fatalf("dry-run file exists: %v", err)
	}
}

func TestDiskBenchmarkRequiresForceOnRootFilesystem(t *testing.T) {
	directory := t.TempDir()
	files := fakeFiles{stats: core.FileSystemStats{AvailableBytes: 10 << 30}, files: map[string]string{"/proc/self/mountinfo": "29 23 8:2 / / rw - xfs /dev/sda2 rw\n"}}
	runner := &Runner{deps: core.Dependencies{Files: files, Processes: &fakeProcesses{}, OperatingSystem: "linux"}}
	output, err := runner.Execute(context.Background(), defaultOperation(directory))
	if err != nil || len(output.Errors) != 1 || output.Errors[0].Code != v1alpha1.ErrorInvalidArgument {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	op := defaultOperation(directory)
	op.Options["force"] = json.RawMessage(`true`)
	forced, err := runner.Execute(context.Background(), op)
	if err != nil || len(forced.Errors) != 0 {
		t.Fatalf("forced=%#v err=%v", forced, err)
	}
}

func TestBenchmarkMountpointChoosesLongestMatch(t *testing.T) {
	files := fakeFiles{files: map[string]string{"/proc/self/mountinfo": "29 23 8:2 / / rw - xfs /dev/sda2 rw\n30 29 8:3 / /data rw - xfs /dev/sdb1 rw\n"}}
	if got := benchmarkMountpoint(files, "/data/jobs/run"); got != "/data" {
		t.Fatalf("mountpoint = %q", got)
	}
	if got := benchmarkMountpoint(files, "/var/lib"); got != "/" {
		t.Fatalf("root mountpoint = %q", got)
	}
}

func TestParseFIOJSON(t *testing.T) {
	result, err := parseFIOJSON([]byte(fioFixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.FIOVersion != "fio-3.38" || result.Write.IOPS != 25600.5 || result.Write.BytesPerSecond != 104857600 {
		t.Fatalf("result = %#v", result)
	}
	if result.Latency.AverageMicros != 320 || result.Latency.P95Micros != 520 || result.Latency.P99Micros != 810 || result.DiskUtilPercent != 97.5 {
		t.Fatalf("result = %#v", result)
	}
}

func TestDiskBenchmarkUsesSafeTemporaryFileAndDefaults(t *testing.T) {
	directory := t.TempDir()
	processes := &fakeProcesses{}
	runner := &Runner{deps: core.Dependencies{Files: fakeFiles{stats: core.FileSystemStats{AvailableBytes: 10 << 30}}, Processes: processes, OperatingSystem: "linux"}}
	output, err := runner.Execute(context.Background(), defaultOperation(directory))
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Errors) != 0 || len(output.Items) != 1 {
		t.Fatalf("output = %#v", output)
	}
	var result DiskBenchmarkResult
	if err := json.Unmarshal(output.Items[0].Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Profile != "rand-write" || result.BlockSize != "4K" || result.SizeBytes != 1<<30 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(result.File); !os.IsNotExist(err) {
		t.Fatalf("temporary file was not removed: %v", err)
	}
	processes.mu.Lock()
	specs := append([]core.ProcessSpec(nil), processes.specs...)
	processes.mu.Unlock()
	if len(specs) != 1 || specs[0].Path != "/usr/bin/fio" {
		t.Fatalf("specs = %#v", specs)
	}
	joined := strings.Join(specs[0].Args, " ")
	for _, expected := range []string{"--rw=randwrite", "--bs=4K", "--direct=1", "--runtime=10000ms"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("args missing %s: %s", expected, joined)
		}
	}
}

func TestDiskBenchmarkNeverOverwritesExistingFile(t *testing.T) {
	directory := t.TempDir()
	name := filepath.Join(directory, ".toolctl-bench-op-test.fio")
	if err := os.WriteFile(name, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{deps: core.Dependencies{Files: fakeFiles{stats: core.FileSystemStats{AvailableBytes: 10 << 30}}, Processes: &fakeProcesses{}, OperatingSystem: "linux"}}
	output, err := runner.Execute(context.Background(), defaultOperation(directory))
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Errors) != 1 || output.Errors[0].Code != v1alpha1.ErrorExecutionFailed {
		t.Fatalf("output = %#v", output)
	}
	data, _ := os.ReadFile(name)
	if string(data) != "keep" {
		t.Fatalf("existing file changed: %q", data)
	}
}

func TestReadProfileRunsPreparationPass(t *testing.T) {
	directory := t.TempDir()
	processes := &fakeProcesses{}
	op := defaultOperation(directory)
	op.Options["profile"] = json.RawMessage(`"rand-read"`)
	runner := &Runner{deps: core.Dependencies{Files: fakeFiles{stats: core.FileSystemStats{AvailableBytes: 10 << 30}}, Processes: processes, OperatingSystem: "linux"}}
	output, err := runner.Execute(context.Background(), op)
	if err != nil || len(output.Errors) != 0 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	processes.mu.Lock()
	specs := append([]core.ProcessSpec(nil), processes.specs...)
	processes.mu.Unlock()
	if len(specs) != 2 || !strings.Contains(strings.Join(specs[0].Args, " "), "--name=toolctl-prepare") {
		t.Fatalf("specs = %#v", specs)
	}
}

func TestParseSize(t *testing.T) {
	for input, expected := range map[string]uint64{"64M": 64 << 20, "1G": 1 << 30, "1.5GiB": 1536 << 20} {
		value, err := parseSize(input)
		if err != nil || value != expected {
			t.Fatalf("parseSize(%q)=%d,%v", input, value, err)
		}
	}
}

func TestCPUBenchmarkProducesBoundedResult(t *testing.T) {
	runner := &Runner{deps: core.Dependencies{LogicalCPUs: 2}}
	op := v1alpha1.Operation{Capability: capabilityCPURun, TimeoutMS: 5000, Options: map[string]json.RawMessage{
		"duration": json.RawMessage(`100`), "threads": json.RawMessage(`1`),
	}}
	output, err := runner.Execute(context.Background(), op)
	if err != nil || len(output.Errors) != 0 || len(output.Items) != 1 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	var result CPUBenchmarkResult
	if err := json.Unmarshal(output.Items[0].Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Threads != 1 || result.Hashes == 0 || result.BytesPerSecond == 0 || result.Checksum == "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestMemoryBenchmarkProducesBandwidthAndLatency(t *testing.T) {
	runner := &Runner{deps: core.Dependencies{LogicalCPUs: 2}}
	op := v1alpha1.Operation{Capability: capabilityMemoryRun, TimeoutMS: 5000, Options: map[string]json.RawMessage{
		"duration": json.RawMessage(`100`), "threads": json.RawMessage(`1`), "size": json.RawMessage(`"8M"`),
	}}
	output, err := runner.Execute(context.Background(), op)
	if err != nil || len(output.Errors) != 0 || len(output.Items) != 1 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	var result MemoryBenchmarkResult
	if err := json.Unmarshal(output.Items[0].Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.SizeBytes != 8<<20 || result.CopyBytesPerSecond == 0 || result.RandomReads == 0 || result.RandomReadLatencyNs <= 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRawDeviceBenchmarkRejectsRegularFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-device")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	op := defaultOperation(file)
	op.Capability = capabilityDeviceRun
	op.Options["profile"] = json.RawMessage(`"seq-read"`)
	op.Options["destroy-data"] = json.RawMessage(`false`)
	runner := &Runner{deps: core.Dependencies{Files: fakeFiles{}, Processes: &fakeProcesses{}, OperatingSystem: "linux"}}
	output, err := runner.Execute(context.Background(), op)
	if err != nil || len(output.Errors) != 1 || output.Errors[0].Code != v1alpha1.ErrorInvalidArgument {
		t.Fatalf("output=%#v err=%v", output, err)
	}
}

func TestRawDeviceWriteRequiresDestroyData(t *testing.T) {
	op := defaultOperation("/dev/example")
	op.Options["profile"] = json.RawMessage(`"rand-write"`)
	op.Options["destroy-data"] = json.RawMessage(`false`)
	options, profile, err := parseDeviceOptions(op)
	if err != nil || options.DestroyData || profile.RW != "randwrite" {
		t.Fatalf("options=%#v profile=%#v err=%v", options, profile, err)
	}
	if !deviceReferenced("36 29 8:1 / /data rw - xfs /dev/sdb1 rw", "sdb", "/dev/sdb") {
		t.Fatal("partition reference was not detected")
	}
}

func TestBurnMixedProducesBoundedWork(t *testing.T) {
	runner := &Runner{deps: core.Dependencies{LogicalCPUs: 2}}
	op := v1alpha1.Operation{Capability: capabilityBurnRun, Name: "mixed", TimeoutMS: 5000, Options: map[string]json.RawMessage{
		"duration": json.RawMessage(`100`), "threads": json.RawMessage(`1`), "size": json.RawMessage(`"8M"`),
	}}
	output, err := runner.Execute(context.Background(), op)
	if err != nil || len(output.Errors) != 0 || len(output.Items) != 1 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	var result BurnResult
	if err := json.Unmarshal(output.Items[0].Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Mode != "mixed" || result.Hashes == 0 || result.MemoryBytes == 0 || result.Checksum == 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestNetworkEndpoint(t *testing.T) {
	tests := map[string]string{
		"server.example":      "server.example:9234",
		"server.example:9000": "server.example:9000",
		"2001:db8::1":         "[2001:db8::1]:9234",
		"[2001:db8::1]:9000":  "[2001:db8::1]:9000",
	}
	for input, expected := range tests {
		actual, err := networkEndpoint(input, 9234)
		if err != nil || actual != expected {
			t.Fatalf("networkEndpoint(%q)=%q,%v; want %q", input, actual, err, expected)
		}
	}
	if _, err := networkEndpoint("", 9234); err == nil {
		t.Fatal("empty peer was accepted")
	}
	if _, err := networkEndpoint("server.example:not-a-port", 9234); err == nil {
		t.Fatal("invalid explicit port was accepted")
	}
}

func TestNetworkBenchmarkMultipleStreamsBidirectional(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skip("local TCP listeners are unavailable in this sandbox")
		}
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	runner := &Runner{}
	type serverResult struct {
		output core.RunOutput
		err    error
	}
	serverDone := make(chan serverResult, 1)
	go func() {
		output, err := runner.serveNetworkListener(ctx, listener)
		serverDone <- serverResult{output: output, err: err}
	}()
	op := v1alpha1.Operation{Capability: capabilityNetRun, Name: listener.Addr().String(), TimeoutMS: 8000, Options: map[string]json.RawMessage{
		"duration": json.RawMessage(`1000`), "port": json.RawMessage(`9234`), "parallel": json.RawMessage(`2`),
		"direction": json.RawMessage(`"both"`), "bind": json.RawMessage(`"127.0.0.1"`), "rate": json.RawMessage(`"100Mbps"`), "interval": json.RawMessage(`200`),
	}}
	snapshots := 0
	clientOutput, err := runner.runNetworkClientWithProgress(ctx, op, func(result NetworkBenchmarkResult) error {
		snapshots++
		if result.SentBytes == 0 || result.ReceivedBytes == 0 {
			t.Fatalf("live result=%#v", result)
		}
		return nil
	})
	if err != nil || len(clientOutput.Errors) != 0 || len(clientOutput.Items) != 1 {
		t.Fatalf("client output=%#v err=%v", clientOutput, err)
	}
	if snapshots < 3 {
		t.Fatalf("live snapshots=%d", snapshots)
	}
	server := <-serverDone
	if server.err != nil || len(server.output.Errors) != 0 || len(server.output.Items) != 1 {
		t.Fatalf("server output=%#v err=%v", server.output, server.err)
	}
	for side, item := range map[string]v1alpha1.Item{"client": clientOutput.Items[0], "server": server.output.Items[0]} {
		var result NetworkBenchmarkResult
		if err := json.Unmarshal(item.Data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Streams != 2 || result.Direction != "both" || result.SentBytes == 0 || result.ReceivedBytes == 0 || result.BytesPerSecond == 0 {
			t.Fatalf("%s result=%#v", side, result)
		}
	}
}

func TestNetworkLatencyEchoSession(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skip("local TCP listeners are unavailable in this sandbox")
		}
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := &Runner{}
	serverDone := make(chan error, 1)
	go func() {
		output, err := runner.serveNetworkListener(ctx, listener)
		if err == nil && len(output.Errors) > 0 {
			err = errors.New(output.Errors[0].Message)
		}
		serverDone <- err
	}()
	op := v1alpha1.Operation{Capability: capabilityNetLatency, Name: listener.Addr().String(), TimeoutMS: 5000, Options: map[string]json.RawMessage{"count": json.RawMessage(`5`), "interval": json.RawMessage(`1`), "port": json.RawMessage(`9234`)}}
	output, err := runner.Execute(ctx, op)
	if err != nil || len(output.Errors) != 0 || len(output.Items) != 1 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	var result NetworkLatencyResult
	if err := json.Unmarshal(output.Items[0].Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 5 || result.MaxMS < result.MinMS || result.P99MS <= 0 {
		t.Fatalf("result=%#v", result)
	}
}

func TestNetworkBenchmarkRejectsInvalidDirectionAndBind(t *testing.T) {
	runner := &Runner{}
	base := v1alpha1.Operation{Capability: capabilityNetRun, Name: "127.0.0.1", Options: map[string]json.RawMessage{
		"duration": json.RawMessage(`1000`), "port": json.RawMessage(`9234`), "parallel": json.RawMessage(`1`),
		"direction": json.RawMessage(`"sideways"`),
	}}
	output, err := runner.Execute(context.Background(), base)
	if err != nil || len(output.Errors) != 1 || output.Errors[0].Code != v1alpha1.ErrorInvalidArgument {
		t.Fatalf("direction output=%#v err=%v", output, err)
	}
	base.Options["direction"] = json.RawMessage(`"send"`)
	base.Options["bind"] = json.RawMessage(`"not-an-ip"`)
	output, err = runner.Execute(context.Background(), base)
	if err != nil || len(output.Errors) != 1 || output.Errors[0].Code != v1alpha1.ErrorInvalidArgument {
		t.Fatalf("bind output=%#v err=%v", output, err)
	}
}

func TestNetworkHandshakeRoundTripAndValidation(t *testing.T) {
	header := networkHandshake{Stream: 3, Streams: 8, Direction: "receive", DurationMS: 30000, Rate: 125000000}
	copy(header.Session[:], []byte("session-12345678"))
	var encoded bytes.Buffer
	if err := writeNetworkHandshake(&encoded, header); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() != networkHeaderSize {
		t.Fatalf("header size=%d", encoded.Len())
	}
	decoded, err := readNetworkHandshake(&encoded)
	if err != nil || decoded != header {
		t.Fatalf("decoded=%#v err=%v want=%#v", decoded, err, header)
	}
	if err := validateNetworkHandshake(decoded); err != nil {
		t.Fatal(err)
	}
	decoded.Streams = 0
	if err := validateNetworkHandshake(decoded); err == nil {
		t.Fatal("zero streams were accepted")
	}
	if oppositeNetworkDirection("send") != "receive" || oppositeNetworkDirection("receive") != "send" || oppositeNetworkDirection("both") != "both" {
		t.Fatal("network direction inversion is incorrect")
	}
}

func TestParseNetworkRate(t *testing.T) {
	for input, expected := range map[string]uint64{"1Gbps": 125000000, "100Mbps": 12500000, "8bps": 1, "1Gibps": 1 << 27, "": 0} {
		actual, err := parseNetworkRate(input)
		if err != nil || actual != expected {
			t.Fatalf("parseNetworkRate(%q)=%d,%v want=%d", input, actual, err, expected)
		}
	}
	if _, err := parseNetworkRate("10MB"); err == nil {
		t.Fatal("rate without a supported bits-per-second suffix was accepted")
	}
}
