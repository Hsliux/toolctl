package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

const (
	capabilityDiskDoctor = "benchmark.disk.doctor"
	capabilityDiskRun    = "benchmark.disk.run"
	capabilityCPURun     = "benchmark.cpu.run"
	capabilityMemoryRun  = "benchmark.memory.run"
	capabilityDeviceRun  = "benchmark.device.run"
	capabilityNetServe   = "benchmark.network.serve"
	capabilityNetRun     = "benchmark.network.run"
	capabilityNetLatency = "benchmark.network.latency"
	capabilityBurnRun    = "stability.burn.run"
)

type Module struct{ deps core.Dependencies }

func New() *Module { return &Module{} }

func (*Module) Info() v1alpha1.ModuleInfo {
	return v1alpha1.ModuleInfo{Name: "benchmark", Version: "0.1.0", MaxConcurrency: 1}
}

func (*Module) Capabilities() []v1alpha1.Capability {
	return []v1alpha1.Capability{
		{
			ID: capabilityNetLatency, Domain: "benchmark", Resource: "network-latency", Verb: "run",
			Command: v1alpha1.CommandPathSpec{Path: []string{"bench", "net", "latency"}}, Summary: "Measure TCP echo round-trip latency to a toolctl benchmark server",
			NameMode: v1alpha1.NameRequired, Mutating: true, Options: []v1alpha1.OptionSpec{
				{Name: "count", Shorthand: "c", Type: v1alpha1.OptionInt, Default: json.RawMessage("100"), Description: "Number of echo samples"},
				{Name: "interval", Shorthand: "i", Type: v1alpha1.OptionDuration, Default: json.RawMessage("10"), Description: "Delay between samples"},
				{Name: "port", Shorthand: "p", Type: v1alpha1.OptionInt, Default: json.RawMessage("9234"), Description: "Server port when peer has no port"},
				{Name: "bind", Type: v1alpha1.OptionString, Description: "Local IPv4 or IPv6 source address"},
			}, Columns: []v1alpha1.ColumnHint{
				{Header: "PEER", Path: "data.peer", Type: v1alpha1.ColumnString, Order: 10}, {Header: "COUNT", Path: "data.count", Type: v1alpha1.ColumnInteger, Order: 20},
				{Header: "MIN(ms)", Path: "data.minMs", Type: v1alpha1.ColumnDecimal, Order: 30}, {Header: "AVG(ms)", Path: "data.averageMs", Type: v1alpha1.ColumnDecimal, Order: 40},
				{Header: "P50(ms)", Path: "data.p50Ms", Type: v1alpha1.ColumnDecimal, Order: 50}, {Header: "P95(ms)", Path: "data.p95Ms", Type: v1alpha1.ColumnDecimal, Order: 60},
				{Header: "P99(ms)", Path: "data.p99Ms", Type: v1alpha1.ColumnDecimal, Order: 70}, {Header: "MAX(ms)", Path: "data.maxMs", Type: v1alpha1.ColumnDecimal, Order: 80},
			},
		},
		{
			ID: capabilityBurnRun, Domain: "stability", Resource: "burn", Verb: "run",
			Command: v1alpha1.CommandPathSpec{Path: []string{"burn"}}, Summary: "Run a bounded CPU, memory, or mixed stability workload",
			NameMode: v1alpha1.NameOptional, Mutating: true, Options: []v1alpha1.OptionSpec{
				{Name: "duration", Shorthand: "d", Type: v1alpha1.OptionDuration, Default: json.RawMessage("20000"), Description: "Stability workload duration"},
				{Name: "threads", Shorthand: "j", Type: v1alpha1.OptionInt, Default: json.RawMessage("0"), Description: "CPU worker threads; 0 uses all visible logical CPUs"},
				{Name: "size", Shorthand: "s", Type: v1alpha1.OptionString, Default: json.RawMessage(`"256M"`), Description: "Allocated memory for mem or mixed mode"},
			}, Columns: []v1alpha1.ColumnHint{
				{Header: "MODE", Path: "data.mode", Type: v1alpha1.ColumnString, Order: 10},
				{Header: "THREADS", Path: "data.threads", Type: v1alpha1.ColumnInteger, Order: 20},
				{Header: "MEMORY", Path: "data.sizeBytes", Type: v1alpha1.ColumnBytes, Order: 30},
				{Header: "DURATION(ms)", Path: "data.durationMs", Type: v1alpha1.ColumnInteger, Order: 40},
				{Header: "CPU", Path: "data.hashesPerSecond", Type: v1alpha1.ColumnDecimal, Order: 50},
				{Header: "MEM COPY", Path: "data.memoryBytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 60},
			},
		},
		{
			ID: capabilityNetRun, Domain: "benchmark", Resource: "network", Verb: "run",
			Command: v1alpha1.CommandPathSpec{Path: []string{"bench", "net"}}, Summary: "Measure TCP throughput to a toolctl benchmark server",
			NameMode: v1alpha1.NameRequired, Mutating: true, Options: []v1alpha1.OptionSpec{
				{Name: "duration", Shorthand: "d", Type: v1alpha1.OptionDuration, Default: json.RawMessage("10000"), Description: "TCP transfer duration"},
				{Name: "port", Shorthand: "p", Type: v1alpha1.OptionInt, Default: json.RawMessage("9234"), Description: "Server port when peer has no port"},
				{Name: "parallel", Shorthand: "P", Type: v1alpha1.OptionInt, Default: json.RawMessage("1"), Description: "Parallel TCP streams"},
				{Name: "direction", Type: v1alpha1.OptionString, Default: json.RawMessage(`"send"`), Description: "Client traffic direction: send, receive, or both"},
				{Name: "bind", Type: v1alpha1.OptionString, Description: "Local IPv4 or IPv6 source address"},
				{Name: "rate", Type: v1alpha1.OptionString, Description: "Per-direction aggregate rate limit, for example 1Gbps"},
				{Name: "interval", Shorthand: "i", Type: v1alpha1.OptionDuration, Default: json.RawMessage("1000"), Description: "Live reporting interval"},
				{Name: "live", Shorthand: "l", Type: v1alpha1.OptionBool, Default: json.RawMessage("false"), Description: "Print throughput snapshots during the bounded test"},
			}, Columns: []v1alpha1.ColumnHint{
				{Header: "PEER", Path: "data.peer", Type: v1alpha1.ColumnString, Order: 10},
				{Header: "DIRECTION", Path: "data.direction", Type: v1alpha1.ColumnString, Order: 20},
				{Header: "STREAMS", Path: "data.streams", Type: v1alpha1.ColumnInteger, Order: 30},
				{Header: "SEND", Path: "data.sentBytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 40},
				{Header: "RECEIVE", Path: "data.receivedBytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 50},
				{Header: "TOTAL", Path: "data.bytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 60},
				{Header: "DURATION(ms)", Path: "data.durationMs", Type: v1alpha1.ColumnInteger, Order: 70},
				{Header: "BYTES", Path: "data.bytes", Type: v1alpha1.ColumnBytes, Wide: true, Order: 80},
				{Header: "CONNECT(ms)", Path: "data.connectLatencyMs", Type: v1alpha1.ColumnDecimal, Wide: true, Order: 90},
			},
		},
		{
			ID: capabilityNetServe, Domain: "benchmark", Resource: "network", Verb: "serve",
			Command: v1alpha1.CommandPathSpec{Path: []string{"bench", "net", "serve"}}, Summary: "Serve one bounded toolctl TCP benchmark session",
			Mutating: true, Options: []v1alpha1.OptionSpec{
				{Name: "listen", Type: v1alpha1.OptionString, Default: json.RawMessage(`":9234"`), Description: "TCP listen address"},
			}, Columns: []v1alpha1.ColumnHint{
				{Header: "PEER", Path: "data.peer", Type: v1alpha1.ColumnString, Order: 10},
				{Header: "DIRECTION", Path: "data.direction", Type: v1alpha1.ColumnString, Order: 20},
				{Header: "STREAMS", Path: "data.streams", Type: v1alpha1.ColumnInteger, Order: 30},
				{Header: "SEND", Path: "data.sentBytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 40},
				{Header: "RECEIVE", Path: "data.receivedBytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 50},
				{Header: "TOTAL", Path: "data.bytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 60},
				{Header: "DURATION(ms)", Path: "data.durationMs", Type: v1alpha1.ColumnInteger, Order: 70},
			},
		},
		{
			ID: capabilityCPURun, Domain: "benchmark", Resource: "cpu", Verb: "run",
			Command: v1alpha1.CommandPathSpec{Path: []string{"bench", "cpu"}}, Summary: "Measure deterministic SHA-256 CPU throughput without external tools",
			Mutating: true, Options: []v1alpha1.OptionSpec{
				{Name: "duration", Shorthand: "d", Type: v1alpha1.OptionDuration, Default: json.RawMessage("10000"), Description: "Measured CPU workload duration"},
				{Name: "threads", Shorthand: "j", Type: v1alpha1.OptionInt, Default: json.RawMessage("0"), Description: "Worker threads; 0 uses all visible logical CPUs"},
			}, Columns: []v1alpha1.ColumnHint{
				{Header: "THREADS", Path: "data.threads", Type: v1alpha1.ColumnInteger, Order: 10},
				{Header: "DURATION(ms)", Path: "data.durationMs", Type: v1alpha1.ColumnInteger, Order: 20},
				{Header: "HASHES/s", Path: "data.hashesPerSecond", Type: v1alpha1.ColumnDecimal, Order: 30},
				{Header: "THROUGHPUT", Path: "data.bytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 40},
				{Header: "WORK", Path: "data.bytesProcessed", Type: v1alpha1.ColumnBytes, Wide: true, Order: 50},
			},
		},
		{
			ID: capabilityMemoryRun, Domain: "benchmark", Resource: "memory", Verb: "run",
			Command: v1alpha1.CommandPathSpec{Path: []string{"bench", "mem"}}, Summary: "Measure memory copy bandwidth and random-read latency without external tools",
			Mutating: true, Options: []v1alpha1.OptionSpec{
				{Name: "size", Shorthand: "s", Type: v1alpha1.OptionString, Default: json.RawMessage(`"64M"`), Description: "Total memory working-set size"},
				{Name: "duration", Shorthand: "d", Type: v1alpha1.OptionDuration, Default: json.RawMessage("5000"), Description: "Duration of each memory test phase"},
				{Name: "threads", Shorthand: "j", Type: v1alpha1.OptionInt, Default: json.RawMessage("1"), Description: "Worker threads"},
			}, Columns: []v1alpha1.ColumnHint{
				{Header: "THREADS", Path: "data.threads", Type: v1alpha1.ColumnInteger, Order: 10},
				{Header: "SIZE", Path: "data.sizeBytes", Type: v1alpha1.ColumnBytes, Order: 20},
				{Header: "COPY", Path: "data.copyBytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 30},
				{Header: "RANDOM LAT(ns)", Path: "data.randomReadLatencyNs", Type: v1alpha1.ColumnDecimal, Order: 40},
				{Header: "RANDOM OPS/s", Path: "data.randomReadsPerSecond", Type: v1alpha1.ColumnDecimal, Wide: true, Order: 50},
			},
		},
		{
			ID: capabilityDeviceRun, Domain: "benchmark", Resource: "device", Verb: "run",
			Command: v1alpha1.CommandPathSpec{Path: []string{"bench", "device"}}, Summary: "Benchmark an unused raw block device with destructive-write protection",
			NameMode: v1alpha1.NameRequired, Mutating: true, Options: []v1alpha1.OptionSpec{
				{Name: "profile", Shorthand: "p", Type: v1alpha1.OptionString, Default: json.RawMessage(`"seq-read"`), Description: "Workload: seq-read, seq-write, rand-read, rand-write, or rand-rw"},
				{Name: "size", Shorthand: "s", Type: v1alpha1.OptionString, Default: json.RawMessage(`"1G"`), Description: "Device region size to benchmark"},
				{Name: "duration", Shorthand: "d", Type: v1alpha1.OptionDuration, Default: json.RawMessage("10000"), Description: "Measured workload duration"},
				{Name: "warmup", Type: v1alpha1.OptionDuration, Default: json.RawMessage("2000"), Description: "Warm-up duration excluded from results"},
				{Name: "jobs", Shorthand: "j", Type: v1alpha1.OptionInt, Default: json.RawMessage("1"), Description: "Number of fio jobs"},
				{Name: "depth", Shorthand: "q", Type: v1alpha1.OptionInt, Default: json.RawMessage("16"), Description: "I/O queue depth"},
				{Name: "block-size", Shorthand: "b", Type: v1alpha1.OptionString, Default: json.RawMessage(`"auto"`), Description: "I/O block size or auto"},
				{Name: "dry-run", Type: v1alpha1.OptionBool, Default: json.RawMessage("false"), Description: "Validate and show the raw-device fio plan without running fio"},
				{Name: "destroy-data", Type: v1alpha1.OptionBool, Default: json.RawMessage("false"), Description: "Acknowledge irreversible data destruction by a write workload"},
			}, Columns: []v1alpha1.ColumnHint{
				{Header: "MODE", Path: "data.mode", Type: v1alpha1.ColumnString, Order: 5},
				{Header: "PROFILE", Path: "data.profile", Type: v1alpha1.ColumnString, Order: 10},
				{Header: "DEVICE", Path: "data.target", Type: v1alpha1.ColumnString, Order: 20},
				{Header: "READ IOPS", Path: "data.read.iops", Type: v1alpha1.ColumnDecimal, Order: 30},
				{Header: "READ BW", Path: "data.read.bytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 40},
				{Header: "WRITE IOPS", Path: "data.write.iops", Type: v1alpha1.ColumnDecimal, Order: 50},
				{Header: "WRITE BW", Path: "data.write.bytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 60},
				{Header: "P99 LAT(us)", Path: "data.latency.p99Micros", Type: v1alpha1.ColumnDecimal, Order: 70},
				{Header: "UTIL", Path: "data.diskUtilPercent", Type: v1alpha1.ColumnPercent, Wide: true, Order: 80},
				{Header: "DESTRUCTIVE", Path: "data.destructive", Type: v1alpha1.ColumnBoolean, Wide: true, Order: 90},
			},
		},
		{
			ID: capabilityDiskRun, Domain: "benchmark", Resource: "disk", Verb: "run",
			Command: v1alpha1.CommandPathSpec{Path: []string{"bench", "disk"}}, Summary: "Run a safe file-based disk benchmark in a directory",
			NameMode: v1alpha1.NameRequired, Mutating: true,
			Options: []v1alpha1.OptionSpec{
				{Name: "profile", Shorthand: "p", Type: v1alpha1.OptionString, Default: json.RawMessage(`"rand-write"`), Description: "Workload: seq-read, seq-write, rand-read, rand-write, or rand-rw"},
				{Name: "size", Shorthand: "s", Type: v1alpha1.OptionString, Default: json.RawMessage(`"1G"`), Description: "Temporary benchmark file size"},
				{Name: "duration", Shorthand: "d", Type: v1alpha1.OptionDuration, Default: json.RawMessage("10000"), Description: "Measured workload duration"},
				{Name: "warmup", Type: v1alpha1.OptionDuration, Default: json.RawMessage("2000"), Description: "Warm-up duration excluded from results"},
				{Name: "jobs", Shorthand: "j", Type: v1alpha1.OptionInt, Default: json.RawMessage("1"), Description: "Number of fio jobs"},
				{Name: "depth", Shorthand: "q", Type: v1alpha1.OptionInt, Default: json.RawMessage("16"), Description: "I/O queue depth"},
				{Name: "block-size", Shorthand: "b", Type: v1alpha1.OptionString, Default: json.RawMessage(`"auto"`), Description: "I/O block size or auto for the profile default"},
				{Name: "keep-file", Type: v1alpha1.OptionBool, Default: json.RawMessage("false"), Description: "Keep the temporary benchmark file after completion"},
				{Name: "dry-run", Type: v1alpha1.OptionBool, Default: json.RawMessage("false"), Description: "Validate and show the fio plan without creating a file or running fio"},
				{Name: "force", Type: v1alpha1.OptionBool, Default: json.RawMessage("false"), Description: "Allow a write benchmark on the root filesystem"},
			},
			Columns: []v1alpha1.ColumnHint{
				{Header: "MODE", Path: "data.mode", Type: v1alpha1.ColumnString, Order: 5},
				{Header: "PROFILE", Path: "data.profile", Type: v1alpha1.ColumnString, Order: 10},
				{Header: "TARGET", Path: "data.target", Type: v1alpha1.ColumnString, Order: 20},
				{Header: "READ IOPS", Path: "data.read.iops", Type: v1alpha1.ColumnDecimal, Order: 30},
				{Header: "READ BW", Path: "data.read.bytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 40},
				{Header: "WRITE IOPS", Path: "data.write.iops", Type: v1alpha1.ColumnDecimal, Order: 50},
				{Header: "WRITE BW", Path: "data.write.bytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 60},
				{Header: "AVG LAT(us)", Path: "data.latency.averageMicros", Type: v1alpha1.ColumnDecimal, Order: 70},
				{Header: "P95 LAT(us)", Path: "data.latency.p95Micros", Type: v1alpha1.ColumnDecimal, Order: 80},
				{Header: "P99 LAT(us)", Path: "data.latency.p99Micros", Type: v1alpha1.ColumnDecimal, Order: 90},
				{Header: "UTIL", Path: "data.diskUtilPercent", Type: v1alpha1.ColumnPercent, Wide: true, Order: 100},
				{Header: "FILE", Path: "data.file", Type: v1alpha1.ColumnString, Wide: true, Order: 110},
				{Header: "MOUNT", Path: "data.mountpoint", Type: v1alpha1.ColumnString, Wide: true, Order: 120},
				{Header: "ROOTFS", Path: "data.rootFilesystem", Type: v1alpha1.ColumnBoolean, Wide: true, Order: 130},
			},
		},
		{
			ID: capabilityDiskDoctor, Domain: "benchmark", Resource: "disk", Verb: "doctor",
			Command: v1alpha1.CommandPathSpec{Path: []string{"bench", "disk", "doctor"}}, Summary: "Check fio availability for disk benchmarks",
			Columns: []v1alpha1.ColumnHint{
				{Header: "READY", Path: "data.ready", Type: v1alpha1.ColumnBoolean, Order: 10},
				{Header: "FIO", Path: "data.fioPath", Type: v1alpha1.ColumnString, Order: 20},
				{Header: "VERSION", Path: "data.fioVersion", Type: v1alpha1.ColumnString, Order: 30},
				{Header: "MESSAGE", Path: "data.message", Type: v1alpha1.ColumnString, Order: 40},
			},
		},
	}
}

func (m *Module) NewRunner(deps core.Dependencies) (core.Runner, error) {
	if deps.Files == nil || deps.Processes == nil {
		return nil, fmt.Errorf("filesystem and process dependencies are required")
	}
	m.deps = deps
	return &Runner{deps: deps}, nil
}

func (m *Module) Doctor(ctx context.Context) []v1alpha1.CheckResult {
	path, err := m.deps.Processes.LookPath("fio")
	if err != nil {
		return []v1alpha1.CheckResult{{Name: "fio", Status: v1alpha1.CheckWarn, Message: "fio is unavailable", Suggestion: "install the Linux fio package", Details: map[string]string{"capability": "bench disk/device"}}}
	}
	result := m.deps.Processes.Run(ctx, core.ProcessSpec{Path: path, Args: []string{"--version"}, MaxStdout: 64 << 10, MaxStderr: 64 << 10})
	version := strings.TrimSpace(string(result.Stdout))
	ready := result.Err == nil && result.ExitCode == 0 && strings.HasPrefix(version, "fio-")
	if !ready {
		return []v1alpha1.CheckResult{{Name: "fio", Status: v1alpha1.CheckWarn, Message: "the fio executable is not the expected Linux fio tool", Suggestion: "check PATH and install fio from the system package", Details: map[string]string{"path": path, "version": version, "capability": "bench disk/device"}}}
	}
	return []v1alpha1.CheckResult{{Name: "fio", Status: v1alpha1.CheckPass, Message: "fio is ready", Details: map[string]string{"path": path, "version": version, "capability": "bench disk/device"}}}
}

type Runner struct{ deps core.Dependencies }
