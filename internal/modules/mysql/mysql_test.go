package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

type fakeProcesses struct {
	specs []core.ProcessSpec
}

func (f *fakeProcesses) LookPath(name string) (string, error) {
	if name == "mysql" {
		return "/usr/bin/mysql", nil
	}
	return "", errors.New("not found")
}

func (f *fakeProcesses) Run(_ context.Context, spec core.ProcessSpec) core.ProcessResult {
	f.specs = append(f.specs, spec)
	query := spec.Args[len(spec.Args)-1]
	if strings.Contains(query, "memory_summary_global_by_event_name") {
		return core.ProcessResult{ExitCode: 0, Stdout: []byte("2147483648\n")}
	}
	return core.ProcessResult{ExitCode: 0, Stdout: []byte(strings.Join([]string{
		"Uptime\t3600",
		"Threads_connected\t23",
		"Threads_running\t4",
		"Max_used_connections\t80",
		"Connections\t1000",
		"Aborted_connects\t2",
		"Innodb_buffer_pool_bytes_data\t6442450944",
		"Innodb_buffer_pool_read_requests\t10000",
		"Innodb_buffer_pool_reads\t100",
		"version\t8.0.40",
		"max_connections\t200",
		"innodb_buffer_pool_size\t8589934592",
		"innodb_buffer_pool_instances\t8",
		"thread_cache_size\t64",
		"table_open_cache\t4000",
	}, "\n"))}
}

func TestStatusCollectsMemoryPoolAndConnections(t *testing.T) {
	processes := &fakeProcesses{}
	runner := &Runner{processes: processes}
	op := v1alpha1.Operation{Options: map[string]json.RawMessage{
		"host":                json.RawMessage(`"db.example"`),
		"port":                json.RawMessage(`3307`),
		"user":                json.RawMessage(`"observer"`),
		"defaults-extra-file": json.RawMessage(`"/run/secrets/mysql.cnf"`),
		"connect-timeout":     json.RawMessage(`3000`),
	}}
	output, err := runner.Execute(context.Background(), op)
	if err != nil || len(output.Errors) != 0 || len(output.Items) != 1 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	var status Status
	if err := json.Unmarshal(output.Items[0].Data, &status); err != nil {
		t.Fatal(err)
	}
	if status.Instance != "db.example:3307" || status.MemoryBytes != 2147483648 || status.MemorySource != "performance_schema" {
		t.Fatalf("status=%#v", status)
	}
	if status.BufferPoolBytes != 8589934592 || status.BufferPoolUsedPercent != 75 || status.BufferPoolHitPercent != 99 {
		t.Fatalf("buffer pool status=%#v", status)
	}
	if status.Connections != 23 || status.RunningConnections != 4 || status.MaxConnections != 200 || status.MaxUsedConnections != 80 {
		t.Fatalf("connection status=%#v", status)
	}
	if len(processes.specs) != 2 || processes.specs[0].Args[0] != "--defaults-extra-file=/run/secrets/mysql.cnf" {
		t.Fatalf("args=%#v", processes.specs)
	}
	for _, spec := range processes.specs {
		if strings.Contains(strings.Join(spec.Args, " "), "password") {
			t.Fatalf("password option leaked into argv: %#v", spec.Args)
		}
	}
}

func TestSocketOverridesDefaultTCPAddress(t *testing.T) {
	args := clientArgs(options{host: "127.0.0.1", port: 3306, socket: "/run/mysqld.sock", connectTimeout: 3 * time.Second})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--socket=/run/mysqld.sock") || strings.Contains(joined, "--host") || strings.Contains(joined, "--port") || strings.Contains(joined, "--protocol=TCP") {
		t.Fatalf("args=%#v", args)
	}
}

func TestPortWithoutHostForcesTCP(t *testing.T) {
	args := clientArgs(options{port: 3307, connectTimeout: 3 * time.Second})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--protocol=TCP") || !strings.Contains(joined, "--port=3307") {
		t.Fatalf("args=%#v", args)
	}
	if instanceName(options{port: 3307}) != "localhost:3307" {
		t.Fatalf("instance=%q", instanceName(options{port: 3307}))
	}
}

func TestDefaultAddressIsLocalTCP(t *testing.T) {
	opts, err := parseOptions(v1alpha1.Operation{Options: map[string]json.RawMessage{}})
	if err != nil {
		t.Fatal(err)
	}
	if opts.host != "127.0.0.1" || opts.port != 3306 || instanceName(opts) != "127.0.0.1:3306" {
		t.Fatalf("options=%#v", opts)
	}
}
