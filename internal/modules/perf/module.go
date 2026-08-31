package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

const (
	capabilityDoctor            = "system.performance.doctor"
	capabilityCPU               = "system.performance.cpu"
	capabilityDiskIO            = "system.performance.disk-io"
	capabilityNetwork           = "system.performance.network"
	capabilityTCPRetrans        = "system.performance.tcp.retransmission"
	capabilityHistoryDoctor     = "system.performance.history.doctor"
	capabilityHistoryCPU        = "system.performance.history.cpu"
	capabilityHistoryDiskIO     = "system.performance.history.disk-io"
	capabilityHistoryNetwork    = "system.performance.history.network"
	capabilityHistoryTCPRetrans = "system.performance.history.tcp.retransmission"
)

type Module struct{ deps core.Dependencies }

func New() *Module { return &Module{} }

func (*Module) Info() v1alpha1.ModuleInfo {
	return v1alpha1.ModuleInfo{Name: "performance", Version: "0.1.0", MaxConcurrency: 1}
}

func commonOptions(selectorName, selectorDescription string) []v1alpha1.OptionSpec {
	options := []v1alpha1.OptionSpec{
		{Name: "interval", Shorthand: "i", Type: v1alpha1.OptionDuration, Default: json.RawMessage("1000"), Description: "Time between live snapshots"},
		{Name: "count", Shorthand: "c", Type: v1alpha1.OptionInt, Default: json.RawMessage("1"), Description: "Number of live samples to return"},
		{Name: "live", Shorthand: "l", Type: v1alpha1.OptionBool, Default: json.RawMessage("false"), Description: "Continuously print snapshots until interrupted"},
	}
	if selectorName != "" {
		options = append(options, v1alpha1.OptionSpec{Name: selectorName, Type: v1alpha1.OptionStringSlice, Description: selectorDescription})
	}
	return options
}

func historyOptions(selectorName, selectorDescription string) []v1alpha1.OptionSpec {
	options := []v1alpha1.OptionSpec{
		{Name: "from", Type: v1alpha1.OptionString, Description: "Range start in RFC3339 format"},
		{Name: "to", Type: v1alpha1.OptionString, Description: "Range end in RFC3339 format (defaults to now)"},
		{Name: "range", Type: v1alpha1.OptionDuration, Default: json.RawMessage("18000000"), Description: "Historical range when --from is omitted"},
		{Name: "interval", Shorthand: "i", Type: v1alpha1.OptionDuration, Default: json.RawMessage("300000"), Description: "Historical aggregation interval (whole minutes)"},
	}
	if selectorName != "" {
		options = append(options, v1alpha1.OptionSpec{Name: selectorName, Type: v1alpha1.OptionStringSlice, Description: selectorDescription})
	}
	return options
}

func (*Module) Capabilities() []v1alpha1.Capability {
	live := []v1alpha1.Capability{
		{
			ID: capabilityDoctor, Domain: "system", Resource: "performance", Verb: "doctor",
			Command: v1alpha1.CommandPathSpec{Path: []string{"perf", "doctor"}}, Summary: "Check live Linux performance data sources",
			Columns: []v1alpha1.ColumnHint{
				{Header: "READY", Path: "data.ready", Type: v1alpha1.ColumnBoolean, Order: 10},
				{Header: "ARCH", Path: "data.architecture", Type: v1alpha1.ColumnString, Order: 20},
			},
		},
		{
			ID: capabilityCPU, Domain: "system", Resource: "performance-cpu", Verb: "query",
			Command: v1alpha1.CommandPathSpec{Path: []string{"perf", "cpu"}}, Summary: "Sample live CPU utilization from procfs",
			Options: commonOptions("cpu", "CPU names such as cpu,cpu0,cpu1"),
			Columns: metricColumns("CPU", "data.cpu", []v1alpha1.ColumnHint{
				{Header: "USER", Path: "data.userPercent", Type: v1alpha1.ColumnPercent},
				{Header: "SYSTEM", Path: "data.systemPercent", Type: v1alpha1.ColumnPercent},
				{Header: "IOWAIT", Path: "data.ioWaitPercent", Type: v1alpha1.ColumnPercent},
				{Header: "UTIL", Path: "data.utilPercent", Type: v1alpha1.ColumnPercent},
				{Header: "IDLE", Path: "data.idlePercent", Type: v1alpha1.ColumnPercent},
				{Header: "STEAL", Path: "data.stealPercent", Type: v1alpha1.ColumnPercent, Wide: true},
			}),
		},
		{
			ID: capabilityDiskIO, Domain: "system", Resource: "performance-disk-io", Verb: "query",
			Command: v1alpha1.CommandPathSpec{Path: []string{"perf", "io"}}, Summary: "Sample live per-device disk IO from procfs",
			Options: commonOptions("device", "Block device names such as sda,nvme0n1"),
			Columns: metricColumns("DEVICE", "data.device", []v1alpha1.ColumnHint{
				{Header: "READ/s", Path: "data.readsPerSecond", Type: v1alpha1.ColumnDecimal},
				{Header: "WRITE/s", Path: "data.writesPerSecond", Type: v1alpha1.ColumnDecimal},
				{Header: "READ", Path: "data.readBytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond},
				{Header: "WRITE", Path: "data.writeBytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond},
				{Header: "AWAIT(ms)", Path: "data.averageWaitMillis", Type: v1alpha1.ColumnDecimal},
				{Header: "UTIL", Path: "data.utilizationPercent", Type: v1alpha1.ColumnPercent},
				{Header: "QUEUE", Path: "data.averageQueueDepth", Type: v1alpha1.ColumnDecimal, Wide: true},
			}),
		},
		{
			ID: capabilityNetwork, Domain: "system", Resource: "performance-network", Verb: "query",
			Command: v1alpha1.CommandPathSpec{Path: []string{"perf", "net"}}, Summary: "Sample live per-interface traffic from procfs",
			Options: commonOptions("interface", "Network interface names such as eth0,ens3"),
			Columns: metricColumns("INTERFACE", "data.interface", []v1alpha1.ColumnHint{
				{Header: "RECEIVE", Path: "data.receiveBytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond},
				{Header: "TRANSMIT", Path: "data.transmitBytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond},
				{Header: "RXPKT/s", Path: "data.receivePacketsPerSecond", Type: v1alpha1.ColumnDecimal},
				{Header: "TXPKT/s", Path: "data.transmitPacketsPerSecond", Type: v1alpha1.ColumnDecimal},
				{Header: "ERR/s", Path: "data.errorsPerSecond", Type: v1alpha1.ColumnDecimal, Wide: true},
				{Header: "DROP/s", Path: "data.dropsPerSecond", Type: v1alpha1.ColumnDecimal, Wide: true},
			}),
		},
		{
			ID: capabilityTCPRetrans, Domain: "system", Resource: "performance-tcp-retransmission", Verb: "query",
			Command: v1alpha1.CommandPathSpec{Path: []string{"perf", "tcp", "retrans"}}, Summary: "Sample live TCP retransmission from procfs",
			Options: commonOptions("", ""),
			Columns: metricColumns("", "", []v1alpha1.ColumnHint{
				{Header: "OUTSEG/s", Path: "data.segmentsOutPerSecond", Type: v1alpha1.ColumnDecimal},
				{Header: "RETRANS/s", Path: "data.retransmittedPerSecond", Type: v1alpha1.ColumnDecimal},
				{Header: "RETRANS", Path: "data.retransmissionPercent", Type: v1alpha1.ColumnPercent},
			}),
		},
	}
	history := []v1alpha1.Capability{
		{
			ID: capabilityHistoryDoctor, Domain: "system", Resource: "performance-history", Verb: "doctor",
			Command: v1alpha1.CommandPathSpec{Path: []string{"perf", "history", "doctor"}}, Summary: "Check optional ssar history backend",
			Columns: []v1alpha1.ColumnHint{
				{Header: "READY", Path: "data.ready", Type: v1alpha1.ColumnBoolean, Order: 10},
				{Header: "SSAR", Path: "data.ssarPath", Type: v1alpha1.ColumnString, Order: 20},
				{Header: "VERSION", Path: "data.ssarVersion", Type: v1alpha1.ColumnString, Order: 30},
				{Header: "ARCHIVE", Path: "data.archivePath", Type: v1alpha1.ColumnString, Order: 40},
			},
		},
		historyCapability(live[1], capabilityHistoryCPU, []string{"perf", "history", "cpu"}, historyOptions("cpu", "CPU names such as cpu,cpu0,cpu1")),
		historyCapability(live[2], capabilityHistoryDiskIO, []string{"perf", "history", "io"}, historyOptions("device", "Block device names such as sda,nvme0n1")),
		historyCapability(live[3], capabilityHistoryNetwork, []string{"perf", "history", "net"}, historyOptions("interface", "Network interface names such as eth0,ens3")),
		historyCapability(live[4], capabilityHistoryTCPRetrans, []string{"perf", "history", "tcp", "retrans"}, historyOptions("", "")),
	}
	return append(live, history...)
}

func historyCapability(base v1alpha1.Capability, id string, path []string, options []v1alpha1.OptionSpec) v1alpha1.Capability {
	base.ID = id
	base.Command = v1alpha1.CommandPathSpec{Path: path}
	subject := strings.TrimSuffix(strings.TrimPrefix(base.Summary, "Sample live "), " from procfs")
	base.Summary = "Query historical " + subject + " through ssar"
	base.Options = options
	return base
}

func metricColumns(entityHeader, entityPath string, values []v1alpha1.ColumnHint) []v1alpha1.ColumnHint {
	columns := []v1alpha1.ColumnHint{{Header: "TIME", Path: "data.timestamp", Type: v1alpha1.ColumnString, Order: 10}}
	order := 20
	if entityHeader != "" {
		columns = append(columns, v1alpha1.ColumnHint{Header: entityHeader, Path: entityPath, Type: v1alpha1.ColumnString, Order: order})
		order += 10
	}
	for _, column := range values {
		column.Order = order
		order += 10
		columns = append(columns, column)
	}
	return columns
}

func (m *Module) NewRunner(deps core.Dependencies) (core.Runner, error) {
	if deps.Files == nil || deps.Clock == nil || deps.Waiter == nil {
		return nil, fmt.Errorf("filesystem, clock, and waiter dependencies are required")
	}
	m.deps = deps
	return &Runner{deps: deps}, nil
}

func (m *Module) Doctor(ctx context.Context) []v1alpha1.CheckResult {
	checks := []v1alpha1.CheckResult{}
	missing := []string{}
	for _, path := range []string{"/proc/stat", "/proc/diskstats", "/proc/net/dev", "/proc/net/snmp"} {
		if _, err := m.deps.Files.ReadFile(path, maxPseudoFile); err != nil {
			missing = append(missing, path)
		}
	}
	if len(missing) == 0 {
		checks = append(checks, v1alpha1.CheckResult{Name: "procfs", Status: v1alpha1.CheckPass, Message: "live performance data sources are readable", Details: map[string]string{"capability": "perf cpu/io/net/tcp"}})
	} else {
		checks = append(checks, v1alpha1.CheckResult{Name: "procfs", Status: v1alpha1.CheckWarn, Message: "some live performance data sources are unavailable", Suggestion: "check procfs mount and permissions", Details: map[string]string{"missing": strings.Join(missing, ","), "capability": "perf cpu/io/net/tcp"}})
	}
	path, err := m.deps.Processes.LookPath("ssar")
	if err != nil {
		checks = append(checks, v1alpha1.CheckResult{Name: "ssar", Status: v1alpha1.CheckWarn, Message: "optional ssar history tool is unavailable", Suggestion: "install ssar only when perf history is required", Details: map[string]string{"capability": "perf history"}})
		return checks
	}
	version := m.deps.Processes.Run(ctx, core.ProcessSpec{Path: path, Args: []string{"--version"}, MaxStdout: 64 << 10, MaxStderr: 64 << 10})
	versionText := strings.TrimSpace(string(version.Stdout))
	archive := false
	if _, statErr := m.deps.Files.Stat(historyArchivePath); statErr == nil {
		archive = true
	}
	status, message := v1alpha1.CheckPass, "ssar history backend is ready"
	suggestion := ""
	if version.Err != nil || version.ExitCode != 0 || !archive {
		status, message, suggestion = v1alpha1.CheckWarn, "ssar is installed but its history backend is incomplete", "check ssar version and /var/log/sre_proc collection"
	}
	checks = append(checks, v1alpha1.CheckResult{Name: "ssar", Status: status, Message: message, Suggestion: suggestion, Details: map[string]string{"path": path, "version": versionText, "archive": fmt.Sprint(archive), "capability": "perf history"}})
	return checks
}

type Runner struct{ deps core.Dependencies }
