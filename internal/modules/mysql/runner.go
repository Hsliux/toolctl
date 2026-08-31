package mysql

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

const statusQuery = "SHOW GLOBAL STATUS; SHOW GLOBAL VARIABLES;"
const memoryQuery = "SELECT COALESCE(SUM(CURRENT_NUMBER_OF_BYTES_USED),0) FROM performance_schema.memory_summary_global_by_event_name;"

type Status struct {
	Instance              string  `json:"instance"`
	Client                string  `json:"client"`
	Version               string  `json:"version"`
	UptimeSeconds         uint64  `json:"uptimeSeconds"`
	MemoryBytes           uint64  `json:"memoryBytes"`
	MemorySource          string  `json:"memorySource"`
	BufferPoolBytes       uint64  `json:"bufferPoolBytes"`
	BufferPoolDataBytes   uint64  `json:"bufferPoolDataBytes"`
	BufferPoolUsedPercent float64 `json:"bufferPoolUsedPercent"`
	BufferPoolHitPercent  float64 `json:"bufferPoolHitPercent"`
	BufferPoolInstances   uint64  `json:"bufferPoolInstances"`
	Connections           uint64  `json:"connections"`
	RunningConnections    uint64  `json:"runningConnections"`
	MaxConnections        uint64  `json:"maxConnections"`
	MaxUsedConnections    uint64  `json:"maxUsedConnections"`
	TotalConnections      uint64  `json:"totalConnections"`
	AbortedConnects       uint64  `json:"abortedConnects"`
	ThreadCacheSize       uint64  `json:"threadCacheSize"`
	TableOpenCache        uint64  `json:"tableOpenCache"`
}

type Runner struct{ processes core.ProcessExecutor }

type options struct {
	host, user, socket, defaultsFile string
	port                             int64
	connectTimeout                   time.Duration
}

func (r *Runner) Execute(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	opts, err := parseOptions(op)
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), nil), nil
	}
	path, client := findClient(r.processes)
	if path == "" {
		return failure(v1alpha1.ErrorDependencyMissing, "MySQL client is unavailable", map[string]string{"suggestion": "install mysql or mariadb client"}), nil
	}
	baseArgs := clientArgs(opts)
	result := r.run(ctx, path, baseArgs, statusQuery, 4<<20)
	if result.Err != nil || result.ExitCode != 0 {
		return failure(processErrorCode(result), "MySQL status query failed", processDetails(result)), nil
	}
	values := parseVariables(result.Stdout)
	if len(values) == 0 {
		return failure(v1alpha1.ErrorParseFailed, "MySQL client returned no status variables", nil), nil
	}
	status := buildStatus(values, opts, client)
	warnings := []v1alpha1.Diagnostic{}
	memory := r.run(ctx, path, baseArgs, memoryQuery, 64<<10)
	if memory.Err == nil && memory.ExitCode == 0 {
		if value, ok := firstUint(memory.Stdout); ok && value > 0 {
			status.MemoryBytes = value
			status.MemorySource = "performance_schema"
		}
	}
	if status.MemorySource != "performance_schema" {
		warnings = append(warnings, v1alpha1.Diagnostic{Code: "MYSQL_MEMORY_ESTIMATED", Message: "total instrumented memory is unavailable; MEMORY uses the best available server counter", Details: map[string]string{"source": status.MemorySource}})
	}
	item, err := resultbuilder.NewItem("MySQLStatus", status.Instance, status.Instance, status)
	if err != nil {
		return core.RunOutput{}, err
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: warnings, Errors: []v1alpha1.TargetError{}}, nil
}

func (r *Runner) run(ctx context.Context, path string, base []string, query string, max int64) core.ProcessResult {
	args := append(append([]string(nil), base...), "--execute="+query)
	return r.processes.Run(ctx, core.ProcessSpec{Path: path, Args: args, MaxStdout: max, MaxStderr: 256 << 10})
}

func parseOptions(op v1alpha1.Operation) (options, error) {
	result := options{host: "127.0.0.1", port: 3306, connectTimeout: 3 * time.Second}
	var err error
	if result.host, err = stringOption(op, "host", result.host); err != nil {
		return result, err
	}
	if result.user, err = stringOption(op, "user", result.user); err != nil {
		return result, err
	}
	if result.socket, err = stringOption(op, "socket", result.socket); err != nil {
		return result, err
	}
	if result.defaultsFile, err = stringOption(op, "defaults-extra-file", result.defaultsFile); err != nil {
		return result, err
	}
	if result.port, err = intOption(op, "port", result.port); err != nil {
		return result, err
	}
	if raw, ok := op.Options["connect-timeout"]; ok {
		var milliseconds int64
		if err := json.Unmarshal(raw, &milliseconds); err != nil {
			return result, fmt.Errorf("invalid --connect-timeout: %w", err)
		}
		result.connectTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	if result.port < 1 || result.port > 65535 {
		return result, fmt.Errorf("--port must be between 1 and 65535")
	}
	if result.connectTimeout <= 0 {
		return result, fmt.Errorf("--connect-timeout must be greater than zero")
	}
	return result, nil
}

func clientArgs(opts options) []string {
	args := []string{}
	// MySQL requires defaults-file options to precede all other options.
	if opts.defaultsFile != "" {
		args = append(args, "--defaults-extra-file="+opts.defaultsFile)
	}
	args = append(args, "--batch", "--raw", "--skip-column-names", "--connect-timeout="+strconv.FormatInt(maxInt64(1, int64(math.Ceil(opts.connectTimeout.Seconds()))), 10))
	if opts.socket != "" {
		args = append(args, "--socket="+opts.socket)
	} else {
		if opts.host != "" || opts.port != 0 {
			args = append(args, "--protocol=TCP")
		}
		if opts.host != "" {
			args = append(args, "--host="+opts.host)
		}
		if opts.port != 0 {
			args = append(args, "--port="+strconv.FormatInt(opts.port, 10))
		}
	}
	if opts.user != "" {
		args = append(args, "--user="+opts.user)
	}
	return args
}

func parseVariables(output []byte) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(parts) == 2 {
			values[strings.ToLower(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return values
}

func buildStatus(v map[string]string, opts options, client string) Status {
	status := Status{
		Instance: instanceName(opts), Client: client, Version: v["version"], UptimeSeconds: uintValue(v, "uptime"),
		BufferPoolBytes: uintValue(v, "innodb_buffer_pool_size"), BufferPoolDataBytes: uintValue(v, "innodb_buffer_pool_bytes_data"),
		BufferPoolInstances: uintValue(v, "innodb_buffer_pool_instances"), Connections: uintValue(v, "threads_connected"),
		RunningConnections: uintValue(v, "threads_running"), MaxConnections: uintValue(v, "max_connections"),
		MaxUsedConnections: uintValue(v, "max_used_connections"), TotalConnections: uintValue(v, "connections"),
		AbortedConnects: uintValue(v, "aborted_connects"), ThreadCacheSize: uintValue(v, "thread_cache_size"), TableOpenCache: uintValue(v, "table_open_cache"),
	}
	if status.BufferPoolBytes > 0 {
		status.BufferPoolUsedPercent = percent(status.BufferPoolDataBytes, status.BufferPoolBytes)
	}
	requests, reads := uintValue(v, "innodb_buffer_pool_read_requests"), uintValue(v, "innodb_buffer_pool_reads")
	if requests > 0 && reads <= requests {
		status.BufferPoolHitPercent = 100 - percent(reads, requests)
	}
	for _, key := range []string{"global_memory_used", "memory_used", "global_connection_memory"} {
		if value := uintValue(v, key); value > 0 {
			status.MemoryBytes, status.MemorySource = value, key
			break
		}
	}
	if status.MemoryBytes == 0 && status.BufferPoolDataBytes > 0 {
		status.MemoryBytes, status.MemorySource = status.BufferPoolDataBytes, "innodb_buffer_pool_bytes_data"
	}
	if status.MemorySource == "" {
		status.MemorySource = "unavailable"
	}
	return status
}

func instanceName(opts options) string {
	if opts.socket != "" {
		return opts.socket
	}
	if opts.host == "" {
		if opts.port != 0 {
			return fmt.Sprintf("localhost:%d", opts.port)
		}
		return "client-default"
	}
	if opts.port == 0 {
		return opts.host
	}
	return fmt.Sprintf("%s:%d", opts.host, opts.port)
}

func uintValue(values map[string]string, key string) uint64 {
	value, _ := strconv.ParseUint(values[key], 10, 64)
	return value
}
func firstUint(output []byte) (uint64, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	return value, err == nil
}
func percent(part, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) * 100 / float64(total)
}
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func stringOption(op v1alpha1.Operation, name, fallback string) (string, error) {
	raw, ok := op.Options[name]
	if !ok {
		return fallback, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("invalid --%s: %w", name, err)
	}
	return value, nil
}
func intOption(op v1alpha1.Operation, name string, fallback int64) (int64, error) {
	raw, ok := op.Options[name]
	if !ok {
		return fallback, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("invalid --%s: %w", name, err)
	}
	return value, nil
}
func processDetails(result core.ProcessResult) map[string]string {
	details := map[string]string{"exitCode": strconv.Itoa(result.ExitCode)}
	if message := strings.TrimSpace(string(result.Stderr)); message != "" {
		details["stderr"] = message
	}
	if result.TimedOut {
		details["timeout"] = "true"
	}
	return details
}
func processErrorCode(result core.ProcessResult) v1alpha1.ErrorCode {
	if result.TimedOut {
		return v1alpha1.ErrorTimeout
	}
	if result.StdoutExceeded || result.StderrExceeded {
		return v1alpha1.ErrorOutputLimitExceeded
	}
	if strings.Contains(strings.ToLower(string(result.Stderr)), "access denied") {
		return v1alpha1.ErrorAuthenticationFailed
	}
	return v1alpha1.ErrorExecutionFailed
}
func failure(code v1alpha1.ErrorCode, message string, details map[string]string) core.RunOutput {
	if details == nil {
		details = map[string]string{}
	}
	return core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{{Code: code, Message: message, Details: details}}}
}
