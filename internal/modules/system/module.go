package system

import (
	"context"
	"encoding/json"
	"fmt"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

const (
	capabilityHostInfo   = "system.host.info"
	capabilityDiskList   = "system.disk.list"
	capabilityNICList    = "system.network-interface.list"
	capabilityHealth     = "system.health.check"
	capabilityTop        = "system.process.top"
	capabilityDiskHealth = "system.disk.health"
	capabilityNetDNS     = "network.dns.resolve"
	capabilityNetRoute   = "network.route.get"
	capabilityNetConnect = "network.tcp.connect"
)

type Module struct{ deps core.Dependencies }

func New() *Module { return &Module{} }

func (*Module) Info() v1alpha1.ModuleInfo {
	return v1alpha1.ModuleInfo{Name: "system", Version: "0.1.0", MaxConcurrency: 1}
}

func (*Module) Capabilities() []v1alpha1.Capability {
	return []v1alpha1.Capability{
		{
			ID: capabilityNetDNS, Domain: "network", Resource: "dns", Verb: "resolve", Command: v1alpha1.CommandPathSpec{Path: []string{"net", "dns"}}, Summary: "Resolve a hostname and measure lookup latency", NameMode: v1alpha1.NameRequired,
			Columns: []v1alpha1.ColumnHint{{Header: "NAME", Path: "data.name", Type: v1alpha1.ColumnString, Order: 10}, {Header: "ADDRESS", Path: "data.address", Type: v1alpha1.ColumnString, Order: 20}, {Header: "LOOKUP(ms)", Path: "data.latencyMs", Type: v1alpha1.ColumnDecimal, Order: 30}},
		},
		{
			ID: capabilityNetRoute, Domain: "network", Resource: "route", Verb: "get", Command: v1alpha1.CommandPathSpec{Path: []string{"net", "route"}}, Summary: "Show the selected source address and interface for a destination", NameMode: v1alpha1.NameRequired,
			Columns: []v1alpha1.ColumnHint{{Header: "TARGET", Path: "data.target", Type: v1alpha1.ColumnString, Order: 10}, {Header: "ADDRESS", Path: "data.address", Type: v1alpha1.ColumnString, Order: 20}, {Header: "SOURCE", Path: "data.source", Type: v1alpha1.ColumnString, Order: 30}, {Header: "INTERFACE", Path: "data.interface", Type: v1alpha1.ColumnString, Order: 40}, {Header: "GATEWAY", Path: "data.gateway", Type: v1alpha1.ColumnString, Wide: true, Order: 50}},
		},
		{
			ID: capabilityNetConnect, Domain: "network", Resource: "tcp", Verb: "connect", Command: v1alpha1.CommandPathSpec{Path: []string{"net", "connect"}}, Summary: "Test TCP connectivity and connection latency", NameMode: v1alpha1.NameRequired,
			Options: []v1alpha1.OptionSpec{{Name: "connect-timeout", Type: v1alpha1.OptionDuration, Default: json.RawMessage("3000"), Description: "TCP connection timeout"}, {Name: "bind", Type: v1alpha1.OptionString, Description: "Local source IP address"}},
			Columns: []v1alpha1.ColumnHint{{Header: "TARGET", Path: "data.target", Type: v1alpha1.ColumnString, Order: 10}, {Header: "STATUS", Path: "data.status", Type: v1alpha1.ColumnString, Order: 20}, {Header: "LOCAL", Path: "data.local", Type: v1alpha1.ColumnString, Order: 30}, {Header: "REMOTE", Path: "data.remote", Type: v1alpha1.ColumnString, Order: 40}, {Header: "CONNECT(ms)", Path: "data.latencyMs", Type: v1alpha1.ColumnDecimal, Order: 50}},
		},
		{
			ID: capabilityHealth, Domain: "system", Resource: "health", Verb: "check",
			Command: v1alpha1.CommandPathSpec{Path: []string{"health"}}, Summary: "Check current server resource health without external tools",
			Columns: []v1alpha1.ColumnHint{
				{Header: "CHECK", Path: "data.check", Type: v1alpha1.ColumnString, Order: 10},
				{Header: "STATUS", Path: "data.status", Type: v1alpha1.ColumnString, Order: 20},
				{Header: "VALUE", Path: "data.value", Type: v1alpha1.ColumnString, Order: 30},
				{Header: "MESSAGE", Path: "data.message", Type: v1alpha1.ColumnString, Order: 40},
			},
		},
		{
			ID: capabilityTop, Domain: "system", Resource: "process", Verb: "top",
			Command: v1alpha1.CommandPathSpec{Path: []string{"top"}}, Summary: "Show top processes by CPU, memory, or I/O from procfs",
			NameMode: v1alpha1.NameOptional, Options: []v1alpha1.OptionSpec{
				{Name: "interval", Shorthand: "i", Type: v1alpha1.OptionDuration, Default: json.RawMessage("1000"), Description: "Sampling interval for CPU and I/O rates"},
				{Name: "limit", Shorthand: "n", Type: v1alpha1.OptionInt, Default: json.RawMessage("10"), Description: "Maximum processes to show"},
				{Name: "live", Shorthand: "l", Type: v1alpha1.OptionBool, Default: json.RawMessage("false"), Description: "Continuously print process snapshots until interrupted"},
			}, Columns: []v1alpha1.ColumnHint{
				{Header: "PID", Path: "data.pid", Type: v1alpha1.ColumnInteger, Order: 10},
				{Header: "UID", Path: "data.uid", Type: v1alpha1.ColumnInteger, Order: 20},
				{Header: "CPU", Path: "data.cpuPercent", Type: v1alpha1.ColumnPercent, Order: 30},
				{Header: "MEMORY", Path: "data.memoryBytes", Type: v1alpha1.ColumnBytes, Order: 40},
				{Header: "READ", Path: "data.readBytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 50},
				{Header: "WRITE", Path: "data.writeBytesPerSecond", Type: v1alpha1.ColumnBytesPerSecond, Order: 60},
				{Header: "COMMAND", Path: "data.command", Type: v1alpha1.ColumnString, Order: 70},
			},
		},
		{
			ID: capabilityDiskHealth, Domain: "system", Resource: "disk", Verb: "health",
			Command: v1alpha1.CommandPathSpec{Path: []string{"disk", "health"}}, Summary: "Check basic block-device state and queue settings from sysfs",
			Columns: []v1alpha1.ColumnHint{
				{Header: "DEVICE", Path: "data.device", Type: v1alpha1.ColumnString, Order: 10},
				{Header: "STATUS", Path: "data.status", Type: v1alpha1.ColumnString, Order: 20},
				{Header: "STATE", Path: "data.state", Type: v1alpha1.ColumnString, Order: 30},
				{Header: "READONLY", Path: "data.readOnly", Type: v1alpha1.ColumnBoolean, Order: 40},
				{Header: "SCHEDULER", Path: "data.scheduler", Type: v1alpha1.ColumnString, Order: 50},
				{Header: "QUEUE", Path: "data.queueDepth", Type: v1alpha1.ColumnInteger, Wide: true, Order: 60},
				{Header: "TIMEOUT(ms)", Path: "data.timeoutMS", Type: v1alpha1.ColumnInteger, Wide: true, Order: 70},
				{Header: "SMART", Path: "data.smartStatus", Type: v1alpha1.ColumnString, Wide: true, Order: 80},
				{Header: "TEMP(C)", Path: "data.temperatureCelsius", Type: v1alpha1.ColumnDecimal, Wide: true, Order: 90},
				{Header: "USED", Path: "data.percentageUsed", Type: v1alpha1.ColumnPercent, Wide: true, Order: 100},
				{Header: "MEDIA ERRORS", Path: "data.mediaErrors", Type: v1alpha1.ColumnInteger, Wide: true, Order: 110},
				{Header: "TOOL", Path: "data.healthTool", Type: v1alpha1.ColumnString, Wide: true, Order: 120},
			},
		},
		{
			ID:         capabilityHostInfo,
			Domain:     "system",
			Resource:   "host",
			Verb:       "info",
			Command:    v1alpha1.CommandPathSpec{Path: []string{"host"}, Aliases: [][]string{}},
			Summary:    "Show basic server hardware and operating environment information",
			NameMode:   v1alpha1.NameNone,
			TargetMode: v1alpha1.TargetNone,
			Mutating:   false,
			Options:    []v1alpha1.OptionSpec{},
			Columns: []v1alpha1.ColumnHint{
				{Header: "HOST", Path: "data.hostname", Type: v1alpha1.ColumnString, Order: 10},
				{Header: "TYPE", Path: "data.serverType.type", Type: v1alpha1.ColumnString, Order: 20},
				{Header: "MODEL", Path: "data.hardware.model", Type: v1alpha1.ColumnString, Order: 30},
				{Header: "VIRT", Path: "data.serverType.virtualization", Type: v1alpha1.ColumnString, Order: 40},
				{Header: "OS", Path: "data.os.prettyName", Type: v1alpha1.ColumnString, Order: 50},
				{Header: "ARCH", Path: "data.cpu.architecture", Type: v1alpha1.ColumnString, Order: 60},
				{Header: "CPU", Path: "data.cpu.logicalCPUs", Type: v1alpha1.ColumnInteger, Order: 70},
				{Header: "MEMORY", Path: "data.memory.effectiveTotalBytes", Type: v1alpha1.ColumnBytes, Order: 80},
				{Header: "ROOT", Path: "data.rootFilesystem.totalBytes", Type: v1alpha1.ColumnBytes, Order: 90},
				{Header: "DISKS", Path: "data.disks", Type: v1alpha1.ColumnNamedBytesList, Order: 100},
				{Header: "NICS", Path: "data.nicCount", Type: v1alpha1.ColumnInteger, Order: 110},
				{Header: "UPTIME", Path: "data.uptimeSeconds", Type: v1alpha1.ColumnDurationSeconds, Order: 120},
				{Header: "KERNEL", Path: "data.kernel.release", Type: v1alpha1.ColumnString, Wide: true, Order: 130},
				{Header: "CPU MODEL", Path: "data.cpu.model", Type: v1alpha1.ColumnString, Wide: true, Order: 140},
				{Header: "PHYSICAL CORES", Path: "data.cpu.physicalCores", Type: v1alpha1.ColumnInteger, Wide: true, Order: 150},
				{Header: "AVAILABLE MEM", Path: "data.memory.availableBytes", Type: v1alpha1.ColumnBytes, Wide: true, Order: 160},
			},
		},
		{
			ID: capabilityDiskList, Domain: "system", Resource: "disk", Verb: "list",
			Command: v1alpha1.CommandPathSpec{Path: []string{"disks"}, Aliases: [][]string{}},
			Summary: "List visible top-level disks and their sizes", NameMode: v1alpha1.NameNone, TargetMode: v1alpha1.TargetNone,
			Options: []v1alpha1.OptionSpec{},
			Columns: []v1alpha1.ColumnHint{
				{Header: "NAME", Path: "name", Type: v1alpha1.ColumnString, Order: 10},
				{Header: "TYPE", Path: "data.kind", Type: v1alpha1.ColumnString, Order: 20},
				{Header: "SIZE", Path: "data.sizeBytes", Type: v1alpha1.ColumnBytes, Order: 30},
				{Header: "MODEL", Path: "data.model", Type: v1alpha1.ColumnString, Order: 40},
				{Header: "ROTATIONAL", Path: "data.rotational", Type: v1alpha1.ColumnBoolean, Order: 50},
				{Header: "REMOVABLE", Path: "data.removable", Type: v1alpha1.ColumnBoolean, Wide: true, Order: 60},
			},
		},
		{
			ID: capabilityNICList, Domain: "system", Resource: "network-interface", Verb: "list",
			Command: v1alpha1.CommandPathSpec{Path: []string{"nics"}, Aliases: [][]string{}},
			Summary: "List basic network interface information", NameMode: v1alpha1.NameNone, TargetMode: v1alpha1.TargetNone,
			Options: []v1alpha1.OptionSpec{},
			Columns: []v1alpha1.ColumnHint{
				{Header: "NAME", Path: "name", Type: v1alpha1.ColumnString, Order: 10},
				{Header: "TYPE", Path: "data.kind", Type: v1alpha1.ColumnString, Order: 20},
				{Header: "STATE", Path: "data.state", Type: v1alpha1.ColumnString, Order: 30},
				{Header: "MTU", Path: "data.mtu", Type: v1alpha1.ColumnInteger, Order: 40},
				{Header: "SPEED(Mbps)", Path: "data.speedMbps", Type: v1alpha1.ColumnInteger, Order: 50},
				{Header: "DUPLEX", Path: "data.duplex", Type: v1alpha1.ColumnString, Order: 60},
				{Header: "CARRIER", Path: "data.carrier", Type: v1alpha1.ColumnBoolean, Wide: true, Order: 70},
				{Header: "VIRTUAL", Path: "data.virtual", Type: v1alpha1.ColumnBoolean, Wide: true, Order: 80},
			},
		},
	}
}

func (m *Module) NewRunner(deps core.Dependencies) (core.Runner, error) {
	if deps.Files == nil {
		return nil, fmt.Errorf("filesystem dependency is required")
	}
	if deps.Hostname == nil {
		return nil, fmt.Errorf("hostname dependency is required")
	}
	m.deps = deps
	return &Runner{deps: deps}, nil
}

func (m *Module) Doctor(context.Context) []v1alpha1.CheckResult {
	status := v1alpha1.CheckPass
	message := "supported Linux platform"
	suggestion := ""
	if m.deps.OperatingSystem != "linux" || (m.deps.Architecture != "amd64" && m.deps.Architecture != "arm64") {
		status, message, suggestion = v1alpha1.CheckFail, "unsupported operating system or architecture", "use Linux amd64 or Linux arm64"
	}
	checks := []v1alpha1.CheckResult{{Name: "platform", Status: status, Message: message, Suggestion: suggestion, Details: map[string]string{"os": m.deps.OperatingSystem, "arch": m.deps.Architecture, "capability": "core"}}}
	if m.deps.Processes != nil {
		tool, path := "", ""
		for _, candidate := range []string{"smartctl", "nvme"} {
			if found, err := m.deps.Processes.LookPath(candidate); err == nil {
				tool, path = candidate, found
				break
			}
		}
		if tool == "" {
			checks = append(checks, v1alpha1.CheckResult{Name: "disk-health-tools", Status: v1alpha1.CheckWarn, Message: "optional SMART/NVMe health tools are unavailable", Suggestion: "install smartmontools and nvme-cli for deep disk health", Details: map[string]string{"capability": "disk health"}})
		} else {
			checks = append(checks, v1alpha1.CheckResult{Name: "disk-health-tools", Status: v1alpha1.CheckPass, Message: "optional disk health tool is available", Details: map[string]string{"capability": "disk health", "tool": tool, "path": path}})
		}
	}
	return checks
}

type Runner struct {
	deps core.Dependencies
}

func (r *Runner) Execute(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	select {
	case <-ctx.Done():
		return core.RunOutput{}, ctx.Err()
	default:
	}
	switch op.Capability {
	case capabilityNetDNS:
		return r.runNetDNS(ctx, op)
	case capabilityNetRoute:
		return r.runNetRoute(ctx, op)
	case capabilityNetConnect:
		return r.runNetConnect(ctx, op)
	case capabilityHealth:
		return r.runHealth()
	case capabilityTop:
		return r.runTop(ctx, op)
	case capabilityDiskHealth:
		return r.runDiskHealth(ctx)
	case capabilityHostInfo:
		info, warnings := collectHost(r.deps)
		item, err := resultbuilder.NewItem("HostInfo", info.Hostname, "", info)
		if err != nil {
			return core.RunOutput{}, err
		}
		return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: warnings, Errors: []v1alpha1.TargetError{}}, nil
	case capabilityDiskList:
		devices, err := collectBlockDevices(r.deps.Files)
		if err != nil {
			return failedOutput(v1alpha1.ErrorExecutionFailed, "block devices could not be read", err), nil
		}
		items := []v1alpha1.Item{}
		for _, device := range topLevelDisks(devices) {
			item, err := resultbuilder.NewItem("Disk", device.Name, "", device)
			if err != nil {
				return core.RunOutput{}, err
			}
			items = append(items, item)
		}
		return core.RunOutput{Items: items, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
	case capabilityNICList:
		interfaces, err := collectNetworkInterfaces(r.deps.Files)
		if err != nil {
			return failedOutput(v1alpha1.ErrorExecutionFailed, "network interfaces could not be read", err), nil
		}
		items := []v1alpha1.Item{}
		for _, networkInterface := range interfaces {
			item, err := resultbuilder.NewItem("NetworkInterface", networkInterface.Name, "", networkInterface)
			if err != nil {
				return core.RunOutput{}, err
			}
			items = append(items, item)
		}
		return core.RunOutput{Items: items, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
	default:
		return core.RunOutput{}, fmt.Errorf("system module does not support %s", op.Capability)
	}
}

func (r *Runner) ExecuteStream(ctx context.Context, op v1alpha1.Operation, emit func(core.RunOutput) error) error {
	if op.Capability != capabilityTop {
		return fmt.Errorf("system capability %s does not support live streaming", op.Capability)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		output, err := r.runTop(ctx, op)
		if err != nil {
			return err
		}
		if err := emit(output); err != nil {
			return err
		}
		if len(output.Errors) > 0 {
			return nil
		}
	}
}

var _ core.StreamingRunner = (*Runner)(nil)

func failedOutput(code v1alpha1.ErrorCode, message string, err error) core.RunOutput {
	details := map[string]string{}
	if err != nil {
		details["reason"] = err.Error()
	}
	return core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{{
		Code: code, Message: message, Details: details,
	}}}
}
