package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

const capabilityStatus = "database.redis.status"

type Module struct{ deps core.Dependencies }

func New() *Module { return &Module{} }

func (*Module) Info() v1alpha1.ModuleInfo {
	return v1alpha1.ModuleInfo{Name: "redis", Version: "0.1.0", MaxConcurrency: 1}
}

func (*Module) Capabilities() []v1alpha1.Capability {
	return []v1alpha1.Capability{{
		ID: capabilityStatus, Domain: "database", Resource: "redis", Verb: "status",
		Command: v1alpha1.CommandPathSpec{Path: []string{"redis"}},
		Summary: "Show key Redis memory, limit, connection, and workload metrics",
		Options: []v1alpha1.OptionSpec{
			{Name: "host", Shorthand: "H", Type: v1alpha1.OptionString, Default: json.RawMessage(`"127.0.0.1"`), Description: "Server host"},
			{Name: "port", Shorthand: "p", Type: v1alpha1.OptionInt, Default: json.RawMessage("6379"), Description: "Server TCP port"},
			{Name: "socket", Shorthand: "S", Type: v1alpha1.OptionString, Description: "Unix socket path; when set, host and port are ignored"},
			{Name: "user", Shorthand: "u", Type: v1alpha1.OptionString, Description: "ACL username; password is read by redis-cli from REDISCLI_AUTH"},
			{Name: "db", Shorthand: "n", Type: v1alpha1.OptionInt, Default: json.RawMessage("0"), Description: "Database number"},
			{Name: "tls", Type: v1alpha1.OptionBool, Default: json.RawMessage("false"), Description: "Use TLS"},
			{Name: "cacert", Type: v1alpha1.OptionString, Description: "CA certificate path for TLS"},
			{Name: "cert", Type: v1alpha1.OptionString, Description: "Client certificate path for TLS"},
			{Name: "key", Type: v1alpha1.OptionString, Description: "Client private key path for TLS"},
		},
		Columns: []v1alpha1.ColumnHint{
			{Header: "INSTANCE", Path: "data.instance", Type: v1alpha1.ColumnString, Order: 10},
			{Header: "VERSION", Path: "data.version", Type: v1alpha1.ColumnString, Order: 20},
			{Header: "ROLE", Path: "data.role", Type: v1alpha1.ColumnString, Order: 30},
			{Header: "UPTIME", Path: "data.uptimeSeconds", Type: v1alpha1.ColumnDurationSeconds, Order: 40},
			{Header: "MEMORY", Path: "data.memoryBytes", Type: v1alpha1.ColumnBytes, Order: 50},
			{Header: "RSS", Path: "data.rssBytes", Type: v1alpha1.ColumnBytes, Order: 60},
			{Header: "MAX MEMORY", Path: "data.maxMemoryBytes", Type: v1alpha1.ColumnBytes, Order: 70},
			{Header: "MEM USED", Path: "data.maxMemoryUsedPercent", Type: v1alpha1.ColumnPercent, Order: 80},
			{Header: "CONNECTIONS", Path: "data.connections", Type: v1alpha1.ColumnInteger, Order: 90},
			{Header: "MAX CLIENTS", Path: "data.maxClients", Type: v1alpha1.ColumnInteger, Order: 100},
			{Header: "OPS/s", Path: "data.operationsPerSecond", Type: v1alpha1.ColumnInteger, Order: 110},
			{Header: "HIT RATE", Path: "data.hitRatePercent", Type: v1alpha1.ColumnPercent, Order: 120},
			{Header: "POLICY", Path: "data.maxMemoryPolicy", Type: v1alpha1.ColumnString, Wide: true, Order: 130},
			{Header: "FRAGMENTATION", Path: "data.fragmentationRatio", Type: v1alpha1.ColumnDecimal, Wide: true, Order: 140},
			{Header: "BLOCKED", Path: "data.blockedClients", Type: v1alpha1.ColumnInteger, Wide: true, Order: 150},
			{Header: "TOTAL CONN", Path: "data.totalConnections", Type: v1alpha1.ColumnInteger, Wide: true, Order: 160},
			{Header: "REJECTED", Path: "data.rejectedConnections", Type: v1alpha1.ColumnInteger, Wide: true, Order: 170},
			{Header: "EVICTED", Path: "data.evictedKeys", Type: v1alpha1.ColumnInteger, Wide: true, Order: 180},
			{Header: "KEYS", Path: "data.keys", Type: v1alpha1.ColumnInteger, Wide: true, Order: 190},
		},
	}}
}

func (m *Module) NewRunner(deps core.Dependencies) (core.Runner, error) {
	if deps.Processes == nil {
		return nil, fmt.Errorf("process dependency is required")
	}
	m.deps = deps
	return &Runner{processes: deps.Processes}, nil
}

func (m *Module) Doctor(ctx context.Context) []v1alpha1.CheckResult {
	path, err := m.deps.Processes.LookPath("redis-cli")
	if err != nil {
		return []v1alpha1.CheckResult{{Name: "client", Status: v1alpha1.CheckWarn, Message: "redis-cli is unavailable", Suggestion: "install redis-cli when Redis inspection is required", Details: map[string]string{"capability": "redis"}}}
	}
	result := m.deps.Processes.Run(ctx, core.ProcessSpec{Path: path, Args: []string{"--version"}, MaxStdout: 64 << 10, MaxStderr: 64 << 10})
	version := strings.TrimSpace(string(result.Stdout))
	status, message := v1alpha1.CheckPass, "redis-cli is ready"
	if result.Err != nil || result.ExitCode != 0 {
		status, message = v1alpha1.CheckWarn, "redis-cli could not be executed"
	}
	return []v1alpha1.CheckResult{{Name: "client", Status: status, Message: message, Details: map[string]string{"capability": "redis", "path": path, "version": version}}}
}
