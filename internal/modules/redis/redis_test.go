package redis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

type fakeProcesses struct{ specs []core.ProcessSpec }

func (f *fakeProcesses) LookPath(name string) (string, error) {
	if name == "redis-cli" {
		return "/usr/bin/redis-cli", nil
	}
	return "", errors.New("not found")
}

func (f *fakeProcesses) Run(_ context.Context, spec core.ProcessSpec) core.ProcessResult {
	f.specs = append(f.specs, spec)
	if len(spec.Args) >= 3 && spec.Args[len(spec.Args)-3] == "CONFIG" {
		return core.ProcessResult{ExitCode: 0, Stdout: []byte("maxclients\n10000\n")}
	}
	return core.ProcessResult{ExitCode: 0, Stdout: []byte(strings.Join([]string{
		"# Server", "redis_version:7.2.5", "uptime_in_seconds:7200", "# Clients", "connected_clients:42", "blocked_clients:2",
		"# Memory", "used_memory:1073741824", "used_memory_rss:1342177280", "maxmemory:2147483648", "maxmemory_policy:allkeys-lru", "mem_fragmentation_ratio:1.25",
		"# Stats", "total_connections_received:2000", "rejected_connections:3", "instantaneous_ops_per_sec:1500", "keyspace_hits:900", "keyspace_misses:100", "evicted_keys:7", "expired_keys:8",
		"# Replication", "role:master", "# Keyspace", "db0:keys=100,expires=20,avg_ttl=50", "db2:keys=25,expires=0,avg_ttl=0",
	}, "\r\n"))}
}

func TestStatusCollectsMemoryConnectionsAndWorkload(t *testing.T) {
	processes := &fakeProcesses{}
	runner := &Runner{processes: processes}
	output, err := runner.Execute(context.Background(), v1alpha1.Operation{Options: map[string]json.RawMessage{
		"host": json.RawMessage(`"cache.example"`), "port": json.RawMessage(`6380`), "user": json.RawMessage(`"observer"`), "db": json.RawMessage(`0`), "tls": json.RawMessage(`false`),
	}})
	if err != nil || len(output.Errors) != 0 || len(output.Items) != 1 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	var status Status
	if err := json.Unmarshal(output.Items[0].Data, &status); err != nil {
		t.Fatal(err)
	}
	if status.Instance != "cache.example:6380" || status.MemoryBytes != 1073741824 || status.MaxMemoryBytes != 2147483648 || status.MaxMemoryUsedPercent != 50 {
		t.Fatalf("memory status=%#v", status)
	}
	if status.Connections != 42 || status.MaxClients != 10000 || status.OperationsPerSecond != 1500 || status.HitRatePercent != 90 || status.Keys != 125 {
		t.Fatalf("workload status=%#v", status)
	}
	for _, spec := range processes.specs {
		joined := strings.Join(spec.Args, " ")
		if strings.Contains(joined, "--pass") || strings.Contains(joined, "-a ") {
			t.Fatalf("password leaked into argv: %#v", spec.Args)
		}
	}
}

func TestStatusReportsRedisAuthenticationError(t *testing.T) {
	processes := &authFailureProcesses{}
	output, err := (&Runner{processes: processes}).Execute(context.Background(), v1alpha1.Operation{Options: map[string]json.RawMessage{}})
	if err != nil || len(output.Errors) != 1 || output.Errors[0].Code != v1alpha1.ErrorAuthenticationFailed {
		t.Fatalf("output=%#v err=%v", output, err)
	}
}

type authFailureProcesses struct{}

func (*authFailureProcesses) LookPath(string) (string, error) { return "/usr/bin/redis-cli", nil }
func (*authFailureProcesses) Run(context.Context, core.ProcessSpec) core.ProcessResult {
	return core.ProcessResult{ExitCode: 0, Stdout: []byte("NOAUTH Authentication required.\n")}
}
