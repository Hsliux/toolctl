package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

const (
	capabilitySSH            = "batch.ssh.execute"
	capabilityContainers     = "batch.container.execute"
	capabilityContainerStats = "container.resource.list"
)

type Module struct{ deps core.Dependencies }

func New() *Module { return &Module{} }

func (*Module) Info() v1alpha1.ModuleInfo {
	return v1alpha1.ModuleInfo{Name: "batch", Version: "0.1.0", MaxConcurrency: 128}
}

func resultColumns(container bool) []v1alpha1.ColumnHint {
	columns := []v1alpha1.ColumnHint{
		{Header: "TARGET", Path: "data.target", Type: v1alpha1.ColumnString, Order: 10},
		{Header: "STATUS", Path: "data.status", Type: v1alpha1.ColumnString, Order: 20},
		{Header: "EXIT", Path: "data.exitCode", Type: v1alpha1.ColumnInteger, Order: 30},
		{Header: "DURATION(ms)", Path: "data.durationMs", Type: v1alpha1.ColumnInteger, Order: 40},
		{Header: "OUTPUT", Path: "data.summary", Type: v1alpha1.ColumnString, Order: 50},
		{Header: "TRUNCATED", Path: "data.outputTruncated", Type: v1alpha1.ColumnBoolean, Wide: true, Order: 55},
	}
	if container {
		columns = append(columns,
			v1alpha1.ColumnHint{Header: "RUNTIME", Path: "data.runtime", Type: v1alpha1.ColumnString, Wide: true, Order: 60},
			v1alpha1.ColumnHint{Header: "PID", Path: "data.pid", Type: v1alpha1.ColumnInteger, Wide: true, Order: 70},
			v1alpha1.ColumnHint{Header: "SCOPE", Path: "data.scope", Type: v1alpha1.ColumnString, Wide: true, Order: 80},
		)
	}
	return columns
}

func (*Module) Capabilities() []v1alpha1.Capability {
	return []v1alpha1.Capability{
		{
			ID: capabilityContainerStats, Domain: "container", Resource: "resource", Verb: "list",
			Command: v1alpha1.CommandPathSpec{Path: []string{"containers"}}, Summary: "Show cgroup resource usage for detected containers",
			NameMode: v1alpha1.NameOptional, Options: []v1alpha1.OptionSpec{
				{Name: "container", Type: v1alpha1.OptionStringSlice, Description: "Container ID prefixes to select"},
				{Name: "runtime", Type: v1alpha1.OptionStringSlice, Description: "Container runtimes to select"},
			}, Columns: []v1alpha1.ColumnHint{
				{Header: "CONTAINER", Path: "data.container", Type: v1alpha1.ColumnString, Order: 10},
				{Header: "RUNTIME", Path: "data.runtime", Type: v1alpha1.ColumnString, Order: 20},
				{Header: "PID", Path: "data.pid", Type: v1alpha1.ColumnInteger, Order: 30},
				{Header: "CPU", Path: "data.cpuUsageSeconds", Type: v1alpha1.ColumnDecimal, Order: 40},
				{Header: "MEMORY", Path: "data.memoryBytes", Type: v1alpha1.ColumnBytes, Order: 50},
				{Header: "MEM LIMIT", Path: "data.memoryLimitBytes", Type: v1alpha1.ColumnBytes, Wide: true, Order: 60},
				{Header: "PIDS", Path: "data.pids", Type: v1alpha1.ColumnInteger, Order: 70},
				{Header: "READ", Path: "data.readBytes", Type: v1alpha1.ColumnBytes, Order: 80},
				{Header: "WRITE", Path: "data.writeBytes", Type: v1alpha1.ColumnBytes, Order: 90},
			},
		},
		{
			ID: capabilitySSH, Domain: "batch", Resource: "ssh", Verb: "execute",
			Command: v1alpha1.CommandPathSpec{Path: []string{"batch", "ssh"}}, Summary: "Execute one command on multiple SSH hosts",
			PassthroughArgs: true, Mutating: true,
			Options: []v1alpha1.OptionSpec{
				{Name: "hosts", Shorthand: "H", Type: v1alpha1.OptionStringSlice, Description: "Target hostnames or IP addresses"},
				{Name: "host-file", Type: v1alpha1.OptionString, Description: "File containing one target host per line"},
				{Name: "user", Shorthand: "u", Type: v1alpha1.OptionString, Description: "SSH login user"},
				{Name: "port", Shorthand: "p", Type: v1alpha1.OptionInt, Description: "SSH port override; omitted uses OpenSSH config or its default"},
				{Name: "identity", Shorthand: "i", Type: v1alpha1.OptionString, Description: "SSH private key path"},
				{Name: "jobs", Shorthand: "j", Type: v1alpha1.OptionInt, Default: json.RawMessage("16"), Description: "Maximum concurrent SSH processes"},
				{Name: "connect-timeout", Type: v1alpha1.OptionDuration, Default: json.RawMessage("10000"), Description: "SSH connection timeout"},
				{Name: "command-timeout", Type: v1alpha1.OptionDuration, Default: json.RawMessage("30000"), Description: "Per-host command timeout"},
				{Name: "max-output", Type: v1alpha1.OptionInt, Default: json.RawMessage("131072"), Description: "Maximum captured stdout and stderr bytes per host"},
				{Name: "accept-new", Type: v1alpha1.OptionBool, Default: json.RawMessage("false"), Description: "Accept new host keys but reject changed keys"},
			}, Columns: resultColumns(false),
		},
		{
			ID: capabilityContainers, Domain: "batch", Resource: "container", Verb: "execute",
			Command: v1alpha1.CommandPathSpec{Path: []string{"batch", "containers"}}, Summary: "Execute one command in all detected container namespaces",
			PassthroughArgs: true, Mutating: true,
			Options: []v1alpha1.OptionSpec{
				{Name: "container", Type: v1alpha1.OptionStringSlice, Description: "Container ID prefixes to select"},
				{Name: "runtime", Type: v1alpha1.OptionStringSlice, Description: "Container runtimes to select: docker, containerd, cri-o, podman, or container"},
				{Name: "scope", Type: v1alpha1.OptionString, Default: json.RawMessage(`"net"`), Description: "Namespace scope: net (default), exec, fs, or process"},
				{Name: "network-only", Type: v1alpha1.OptionBool, Default: json.RawMessage("false"), Description: "Explicit compatibility alias for --scope net"},
				{Name: "jobs", Shorthand: "j", Type: v1alpha1.OptionInt, Default: json.RawMessage("16"), Description: "Maximum concurrent nsenter processes"},
				{Name: "command-timeout", Type: v1alpha1.OptionDuration, Default: json.RawMessage("30000"), Description: "Per-container command timeout"},
				{Name: "max-output", Type: v1alpha1.OptionInt, Default: json.RawMessage("131072"), Description: "Maximum captured stdout and stderr bytes per container"},
			}, Columns: resultColumns(true),
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
	checks := []v1alpha1.CheckResult{}
	for _, dependency := range []struct {
		name       string
		args       []string
		capability string
	}{{"ssh", []string{"-V"}, "batch ssh"}, {"nsenter", []string{"--version"}, "batch containers"}} {
		path, err := m.deps.Processes.LookPath(dependency.name)
		if err != nil {
			checks = append(checks, v1alpha1.CheckResult{Name: dependency.name, Status: v1alpha1.CheckWarn, Message: dependency.name + " is unavailable", Suggestion: "install " + dependency.name + " to use " + dependency.capability, Details: map[string]string{"capability": dependency.capability}})
			continue
		}
		result := m.deps.Processes.Run(ctx, core.ProcessSpec{Path: path, Args: dependency.args, MaxStdout: 64 << 10, MaxStderr: 64 << 10})
		version := strings.TrimSpace(string(result.Stdout))
		if version == "" {
			version = strings.TrimSpace(string(result.Stderr))
		}
		status, message := v1alpha1.CheckPass, dependency.name+" is ready"
		if dependency.name == "ssh" {
			message = "OpenSSH client is ready; remote endpoints are not checked"
		}
		if dependency.name == "nsenter" {
			message = "nsenter is ready; namespace permission is checked when executing"
		}
		suggestion := ""
		if result.Err != nil || result.ExitCode != 0 {
			status, message, suggestion = v1alpha1.CheckWarn, dependency.name+" was found but could not be executed", "check binary permissions and architecture"
		}
		details := map[string]string{"path": path, "version": version, "capability": dependency.capability}
		if dependency.name == "nsenter" {
			details["effectiveUid"] = fmt.Sprint(os.Geteuid())
		}
		checks = append(checks, v1alpha1.CheckResult{Name: dependency.name, Status: status, Message: message, Suggestion: suggestion, Details: details})
	}
	return checks
}

type Runner struct{ deps core.Dependencies }
