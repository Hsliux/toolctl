package batch

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"reflect"
	"sync"
	"testing"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

type fakeFiles struct {
	files map[string]string
	dirs  map[string][]string
}

func (f *fakeFiles) ReadFile(name string, max int64) ([]byte, error) {
	value, ok := f.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if int64(len(value)) > max {
		return nil, errors.New("too large")
	}
	return []byte(value), nil
}
func (f *fakeFiles) ReadDir(name string) ([]fs.DirEntry, error) {
	values, ok := f.dirs[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	result := []fs.DirEntry{}
	for _, value := range values {
		result = append(result, fakeEntry(value))
	}
	return result, nil
}
func (*fakeFiles) Stat(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
func (*fakeFiles) StatFS(string) (core.FileSystemStats, error) {
	return core.FileSystemStats{}, fs.ErrNotExist
}

type fakeEntry string

func (e fakeEntry) Name() string             { return string(e) }
func (fakeEntry) IsDir() bool                { return true }
func (fakeEntry) Type() fs.FileMode          { return fs.ModeDir }
func (fakeEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrInvalid }

type fakeProcesses struct {
	mu     sync.Mutex
	path   string
	specs  []core.ProcessSpec
	result core.ProcessResult
}

func (f *fakeProcesses) LookPath(string) (string, error) {
	if f.path == "" {
		return "", errors.New("not found")
	}
	return f.path, nil
}
func (f *fakeProcesses) Run(_ context.Context, spec core.ProcessSpec) core.ProcessResult {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	return f.result
}

func TestDiscoverContainersFromCgroups(t *testing.T) {
	dockerID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	containerdID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	files := &fakeFiles{dirs: map[string][]string{"/proc": {"1", "50", "80", "90", "self"}}, files: map[string]string{
		"/proc/1/cgroup":  "0::/system.slice/init.scope\n",
		"/proc/50/cgroup": "0::/system.slice/docker-" + dockerID + ".scope\n",
		"/proc/80/cgroup": "0::/kubepods.slice/cri-containerd-" + containerdID + ".scope\n",
		"/proc/90/cgroup": "0::/system.slice/docker-" + dockerID + ".scope\n",
	}}
	containers, err := discoverContainers(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 2 || containers[0].Runtime != "docker" || containers[0].PID != 50 || containers[1].Runtime != "containerd" || containers[1].PID != 80 {
		t.Fatalf("containers = %#v", containers)
	}
	if containers[0].CgroupPath != "/system.slice/docker-"+dockerID+".scope" {
		t.Fatalf("cgroup path=%q", containers[0].CgroupPath)
	}
}

func TestContainerStatsReadsCgroupV2(t *testing.T) {
	id := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	cgroup := "/system.slice/docker-" + id + ".scope"
	base := "/sys/fs/cgroup" + cgroup
	files := &fakeFiles{dirs: map[string][]string{"/proc": {"55"}}, files: map[string]string{"/proc/55/cgroup": "0::" + cgroup + "\n", base + "/cpu.stat": "usage_usec 2500000\n", base + "/memory.current": "1048576\n", base + "/memory.max": "2097152\n", base + "/pids.current": "7\n", base + "/io.stat": "8:0 rbytes=4096 wbytes=8192 rios=2 wios=3\n"}}
	runner := &Runner{deps: core.Dependencies{Files: files}}
	output, err := runner.containerStats(context.Background(), v1alpha1.Operation{Name: "all", Options: map[string]json.RawMessage{}})
	if err != nil || len(output.Errors) != 0 || len(output.Items) != 1 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	var result ContainerResource
	if err := json.Unmarshal(output.Items[0].Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.CPUUsageSeconds != 2.5 || result.MemoryBytes != 1048576 || result.PIDs != 7 || result.ReadBytes != 4096 || result.WriteBytes != 8192 {
		t.Fatalf("result=%#v", result)
	}
}

func TestSSHExecutionUsesSafeArgumentVector(t *testing.T) {
	processes := &fakeProcesses{path: "/usr/bin/ssh", result: core.ProcessResult{ExitCode: 0, Stdout: []byte("node-ok\n"), Duration: 25 * time.Millisecond}}
	runner := &Runner{deps: core.Dependencies{Files: &fakeFiles{files: map[string]string{}, dirs: map[string][]string{}}, Processes: processes}}
	op := v1alpha1.Operation{Capability: capabilitySSH, Arguments: []string{"uname", "-a"}, Options: map[string]json.RawMessage{
		"hosts": json.RawMessage(`["node2","node1"]`), "user": json.RawMessage(`"root"`), "port": json.RawMessage(`22`), "jobs": json.RawMessage(`2`),
		"connect-timeout": json.RawMessage(`10000`), "command-timeout": json.RawMessage(`30000`), "accept-new": json.RawMessage(`false`),
	}}
	output, err := runner.Execute(context.Background(), op)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Items) != 2 || len(output.Errors) != 0 || output.Items[0].Name != "node1" {
		t.Fatalf("output = %#v", output)
	}
	processes.mu.Lock()
	specs := append([]core.ProcessSpec(nil), processes.specs...)
	processes.mu.Unlock()
	if len(specs) != 2 {
		t.Fatalf("specs = %#v", specs)
	}
	for _, spec := range specs {
		if spec.Path != "/usr/bin/ssh" || spec.Args[len(spec.Args)-2] != "uname" || spec.Args[len(spec.Args)-1] != "-a" {
			t.Fatalf("spec = %#v", spec)
		}
	}
}

func TestSSHDoesNotOverrideConfiguredPortByDefault(t *testing.T) {
	processes := &fakeProcesses{path: "/usr/bin/ssh", result: core.ProcessResult{ExitCode: 0}}
	runner := &Runner{deps: core.Dependencies{Files: &fakeFiles{files: map[string]string{}, dirs: map[string][]string{}}, Processes: processes}}
	op := v1alpha1.Operation{Capability: capabilitySSH, Arguments: []string{"true"}, Options: map[string]json.RawMessage{
		"hosts": json.RawMessage(`["prod_node_01"]`), "jobs": json.RawMessage(`1`),
		"connect-timeout": json.RawMessage(`10000`), "command-timeout": json.RawMessage(`30000`), "accept-new": json.RawMessage(`false`),
	}}
	output, err := runner.Execute(context.Background(), op)
	if err != nil || len(output.Errors) != 0 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	args := processes.specs[0].Args
	for _, argument := range args {
		if argument == "-p" {
			t.Fatalf("default execution unexpectedly overrides SSH config port: %#v", args)
		}
	}
}

func TestContainerExecutionBuildsNsenterArguments(t *testing.T) {
	id := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	files := &fakeFiles{dirs: map[string][]string{"/proc": {"123"}}, files: map[string]string{"/proc/123/cgroup": "0::/system.slice/docker-" + id + ".scope\n"}}
	processes := &fakeProcesses{path: "/usr/bin/nsenter", result: core.ProcessResult{ExitCode: 0}}
	runner := &Runner{deps: core.Dependencies{Files: files, Processes: processes}}
	op := v1alpha1.Operation{Capability: capabilityContainers, Arguments: []string{"id"}, Options: map[string]json.RawMessage{
		"jobs": json.RawMessage(`1`), "command-timeout": json.RawMessage(`30000`), "scope": json.RawMessage(`"exec"`),
	}}
	output, err := runner.Execute(context.Background(), op)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Items) != 1 || len(output.Errors) != 0 {
		t.Fatalf("output = %#v", output)
	}
	spec := processes.specs[0]
	wantPrefix := []string{"--target", "123", "--mount", "--uts", "--ipc", "--net", "--pid", "--root=/proc/123/root", "--wd=/proc/123/root", "--", "id"}
	if len(spec.Args) != len(wantPrefix) {
		t.Fatalf("args = %#v", spec.Args)
	}
	for index := range wantPrefix {
		if spec.Args[index] != wantPrefix[index] {
			t.Fatalf("args = %#v", spec.Args)
		}
	}
}

func TestContainerDefaultKeepsHostMountNamespaceAndEntersNetwork(t *testing.T) {
	id := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	files := &fakeFiles{dirs: map[string][]string{"/proc": {"321"}}, files: map[string]string{"/proc/321/cgroup": "0::/system.slice/docker-" + id + ".scope\n"}}
	processes := &fakeProcesses{path: "/usr/bin/nsenter", result: core.ProcessResult{ExitCode: 0}}
	runner := &Runner{deps: core.Dependencies{Files: files, Processes: processes}}
	op := v1alpha1.Operation{Capability: capabilityContainers, Arguments: []string{"ip", "-4", "a"}, Options: map[string]json.RawMessage{
		"jobs": json.RawMessage(`1`), "command-timeout": json.RawMessage(`30000`),
	}}
	output, err := runner.Execute(context.Background(), op)
	if err != nil || len(output.Errors) != 0 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	want := []string{"--target", "321", "--net", "--", "ip", "-4", "a"}
	if actual := processes.specs[0].Args; !reflect.DeepEqual(actual, want) {
		t.Fatalf("args=%#v want=%#v", actual, want)
	}
}

func TestContainerNamespaceScopes(t *testing.T) {
	tests := []struct {
		scope string
		want  []string
	}{
		{scope: "exec", want: []string{"--target", "42", "--mount", "--uts", "--ipc", "--net", "--pid", "--root=/proc/42/root", "--wd=/proc/42/root", "--"}},
		{scope: "fs", want: []string{"--target", "42", "--mount", "--root=/proc/42/root", "--wd=/proc/42/root", "--"}},
		{scope: "net", want: []string{"--target", "42", "--net", "--"}},
		{scope: "process", want: []string{"--target", "42", "--mount", "--pid", "--root=/proc/42/root", "--wd=/proc/42/root", "--"}},
	}
	for _, test := range tests {
		if actual := containerNamespaceArguments(42, test.scope); !reflect.DeepEqual(actual, test.want) {
			t.Fatalf("scope %s args=%#v want=%#v", test.scope, actual, test.want)
		}
	}
}

func TestContainerScopeValidationAndNetworkCompatibility(t *testing.T) {
	operation := v1alpha1.Operation{Options: map[string]json.RawMessage{}}
	if scope, err := containerScopeOption(operation, false); err != nil || scope != "net" {
		t.Fatalf("default scope=%q err=%v", scope, err)
	}
	operation.Options["scope"] = json.RawMessage(`"fs"`)
	if scope, err := containerScopeOption(operation, false); err != nil || scope != "fs" {
		t.Fatalf("scope=%q err=%v", scope, err)
	}
	if _, err := containerScopeOption(operation, true); err == nil {
		t.Fatal("network-only accepted an incompatible scope")
	}
	operation.Options["scope"] = json.RawMessage(`"unknown"`)
	if _, err := containerScopeOption(operation, false); err == nil {
		t.Fatal("unknown scope was accepted")
	}
	operation.Options["scope"] = json.RawMessage(`"net"`)
	if scope, err := containerScopeOption(operation, true); err != nil || scope != "net" {
		t.Fatalf("compatibility scope=%q err=%v", scope, err)
	}
}

func TestHostValidationRejectsSSHOptions(t *testing.T) {
	if _, err := normalizeHosts([]string{"-oProxyCommand=bad"}); err == nil {
		t.Fatal("expected invalid host")
	}
}

func TestHostValidationAcceptsDNSNamesAndSSHAliases(t *testing.T) {
	hosts, err := normalizeHosts([]string{"ga-xc-test01", "node.example.com.", "prod_node_01"})
	if err != nil || len(hosts) != 3 {
		t.Fatalf("hosts=%#v err=%v", hosts, err)
	}
}

func TestBatchReportsOutputLimitExplicitly(t *testing.T) {
	processes := &fakeProcesses{path: "/usr/bin/ssh", result: core.ProcessResult{ExitCode: -1, Stdout: []byte("truncated"), StdoutExceeded: true}}
	runner := &Runner{deps: core.Dependencies{Processes: processes}}
	output := runner.runTargets(context.Background(), []commandTarget{{Name: "node1", Path: "/usr/bin/ssh", Args: []string{"node1", "journalctl"}}}, 1, time.Second, 4096)
	if len(output.Items) != 1 || len(output.Errors) != 1 || output.Errors[0].Code != v1alpha1.ErrorOutputLimitExceeded {
		t.Fatalf("output = %#v", output)
	}
	var result CommandResult
	if err := json.Unmarshal(output.Items[0].Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "output-limit" || !result.OutputTruncated || !result.StdoutTruncated {
		t.Fatalf("result = %#v", result)
	}
	if len(processes.specs) != 1 || processes.specs[0].MaxStdout != 4096 || processes.specs[0].MaxStderr != 4096 {
		t.Fatalf("specs = %#v", processes.specs)
	}
}
