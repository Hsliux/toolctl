package mysql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

const capabilityStatus = "database.mysql.status"

type Module struct{ deps core.Dependencies }

func New() *Module { return &Module{} }

func (*Module) Info() v1alpha1.ModuleInfo {
	return v1alpha1.ModuleInfo{Name: "mysql", Version: "0.1.0", MaxConcurrency: 1}
}

func (*Module) Capabilities() []v1alpha1.Capability {
	return []v1alpha1.Capability{{
		ID: capabilityStatus, Domain: "database", Resource: "mysql", Verb: "status",
		Command: v1alpha1.CommandPathSpec{Path: []string{"mysql"}},
		Summary: "Show key MySQL or MariaDB memory, pool, and connection metrics",
		Options: []v1alpha1.OptionSpec{
			{Name: "host", Shorthand: "H", Type: v1alpha1.OptionString, Default: json.RawMessage(`"127.0.0.1"`), Description: "Server host"},
			{Name: "port", Shorthand: "p", Type: v1alpha1.OptionInt, Default: json.RawMessage("3306"), Description: "Server TCP port"},
			{Name: "user", Shorthand: "u", Type: v1alpha1.OptionString, Description: "Login user; omitted uses MySQL client defaults"},
			{Name: "socket", Shorthand: "S", Type: v1alpha1.OptionString, Description: "Unix socket path; when set, host and port are ignored"},
			{Name: "defaults-extra-file", Type: v1alpha1.OptionString, Description: "Additional MySQL option file containing connection settings"},
			{Name: "connect-timeout", Type: v1alpha1.OptionDuration, Default: json.RawMessage("3000"), Description: "Client connection timeout"},
		},
		Columns: []v1alpha1.ColumnHint{
			{Header: "INSTANCE", Path: "data.instance", Type: v1alpha1.ColumnString, Order: 10},
			{Header: "VERSION", Path: "data.version", Type: v1alpha1.ColumnString, Order: 20},
			{Header: "UPTIME", Path: "data.uptimeSeconds", Type: v1alpha1.ColumnDurationSeconds, Order: 30},
			{Header: "MEMORY", Path: "data.memoryBytes", Type: v1alpha1.ColumnBytes, Order: 40},
			{Header: "BUFFER POOL", Path: "data.bufferPoolBytes", Type: v1alpha1.ColumnBytes, Order: 50},
			{Header: "POOL USED", Path: "data.bufferPoolUsedPercent", Type: v1alpha1.ColumnPercent, Order: 60},
			{Header: "CONNECTIONS", Path: "data.connections", Type: v1alpha1.ColumnInteger, Order: 70},
			{Header: "RUNNING", Path: "data.runningConnections", Type: v1alpha1.ColumnInteger, Order: 80},
			{Header: "MAX CONN", Path: "data.maxConnections", Type: v1alpha1.ColumnInteger, Order: 90},
			{Header: "MAX USED", Path: "data.maxUsedConnections", Type: v1alpha1.ColumnInteger, Order: 100},
			{Header: "POOL HIT", Path: "data.bufferPoolHitPercent", Type: v1alpha1.ColumnPercent, Order: 110},
			{Header: "MEMORY SOURCE", Path: "data.memorySource", Type: v1alpha1.ColumnString, Wide: true, Order: 120},
			{Header: "POOL DATA", Path: "data.bufferPoolDataBytes", Type: v1alpha1.ColumnBytes, Wide: true, Order: 130},
			{Header: "POOL INSTANCES", Path: "data.bufferPoolInstances", Type: v1alpha1.ColumnInteger, Wide: true, Order: 140},
			{Header: "THREAD CACHE", Path: "data.threadCacheSize", Type: v1alpha1.ColumnInteger, Wide: true, Order: 150},
			{Header: "TABLE CACHE", Path: "data.tableOpenCache", Type: v1alpha1.ColumnInteger, Wide: true, Order: 160},
			{Header: "TOTAL CONN", Path: "data.totalConnections", Type: v1alpha1.ColumnInteger, Wide: true, Order: 170},
			{Header: "ABORTED", Path: "data.abortedConnects", Type: v1alpha1.ColumnInteger, Wide: true, Order: 180},
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
	path, client := findClient(m.deps.Processes)
	if path == "" {
		return []v1alpha1.CheckResult{{Name: "client", Status: v1alpha1.CheckWarn, Message: "MySQL client is unavailable", Suggestion: "install mysql or mariadb client when database inspection is required", Details: map[string]string{"capability": "mysql"}}}
	}
	result := m.deps.Processes.Run(ctx, core.ProcessSpec{Path: path, Args: []string{"--version"}, MaxStdout: 64 << 10, MaxStderr: 64 << 10})
	version := strings.TrimSpace(string(result.Stdout))
	status, message := v1alpha1.CheckPass, "MySQL client is ready"
	if result.Err != nil || result.ExitCode != 0 {
		status, message = v1alpha1.CheckWarn, "MySQL client could not be executed"
	}
	return []v1alpha1.CheckResult{{Name: "client", Status: status, Message: message, Details: map[string]string{"capability": "mysql", "tool": client, "path": path, "version": version}}}
}

func findClient(processes core.ProcessExecutor) (string, string) {
	if processes == nil {
		return "", ""
	}
	for _, name := range []string{"mysql", "mariadb"} {
		if path, err := processes.LookPath(name); err == nil {
			return path, name
		}
	}
	return "", ""
}
