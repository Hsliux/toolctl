package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

type Status struct {
	Instance             string  `json:"instance"`
	Version              string  `json:"version"`
	Role                 string  `json:"role"`
	UptimeSeconds        uint64  `json:"uptimeSeconds"`
	MemoryBytes          uint64  `json:"memoryBytes"`
	RSSBytes             uint64  `json:"rssBytes"`
	MaxMemoryBytes       uint64  `json:"maxMemoryBytes"`
	MaxMemoryUsedPercent float64 `json:"maxMemoryUsedPercent"`
	MaxMemoryPolicy      string  `json:"maxMemoryPolicy"`
	FragmentationRatio   float64 `json:"fragmentationRatio"`
	Connections          uint64  `json:"connections"`
	BlockedClients       uint64  `json:"blockedClients"`
	MaxClients           uint64  `json:"maxClients"`
	TotalConnections     uint64  `json:"totalConnections"`
	RejectedConnections  uint64  `json:"rejectedConnections"`
	OperationsPerSecond  uint64  `json:"operationsPerSecond"`
	HitRatePercent       float64 `json:"hitRatePercent"`
	EvictedKeys          uint64  `json:"evictedKeys"`
	ExpiredKeys          uint64  `json:"expiredKeys"`
	Keys                 uint64  `json:"keys"`
}

type Runner struct{ processes core.ProcessExecutor }

type options struct {
	host, socket, user, cacert, cert, key string
	port, database                        int64
	tls                                   bool
}

func (r *Runner) Execute(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	opts, err := parseOptions(op)
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), nil), nil
	}
	path, err := r.processes.LookPath("redis-cli")
	if err != nil {
		return failure(v1alpha1.ErrorDependencyMissing, "redis-cli is unavailable", map[string]string{"suggestion": "install redis-cli"}), nil
	}
	base := clientArgs(opts)
	result := r.processes.Run(ctx, core.ProcessSpec{Path: path, Args: append(append([]string(nil), base...), "INFO", "ALL"), MaxStdout: 4 << 20, MaxStderr: 256 << 10})
	if result.Err != nil || result.ExitCode != 0 {
		return failure(processErrorCode(result), "Redis INFO query failed", processDetails(result)), nil
	}
	values := parseInfo(result.Stdout)
	if isRedisError(result.Stdout) || values["redis_version"] == "" {
		details := map[string]string{}
		if message := strings.TrimSpace(string(result.Stdout)); message != "" {
			details["response"] = message
		}
		code := v1alpha1.ErrorExecutionFailed
		if isRedisAuthenticationError(result.Stdout) {
			code = v1alpha1.ErrorAuthenticationFailed
		}
		return failure(code, "Redis INFO query was rejected or returned an unsupported response", details), nil
	}
	status := buildStatus(values, opts)
	if status.MaxClients == 0 {
		config := r.processes.Run(ctx, core.ProcessSpec{Path: path, Args: append(append([]string(nil), base...), "CONFIG", "GET", "maxclients"), MaxStdout: 64 << 10, MaxStderr: 64 << 10})
		if config.Err == nil && config.ExitCode == 0 {
			status.MaxClients = parseConfigUint(config.Stdout, "maxclients")
		}
	}
	item, err := resultbuilder.NewItem("RedisStatus", status.Instance, status.Instance, status)
	if err != nil {
		return core.RunOutput{}, err
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
}

func parseOptions(op v1alpha1.Operation) (options, error) {
	result := options{host: "127.0.0.1", port: 6379}
	var err error
	for name, destination := range map[string]*string{"host": &result.host, "socket": &result.socket, "user": &result.user, "cacert": &result.cacert, "cert": &result.cert, "key": &result.key} {
		if *destination, err = stringOption(op, name, *destination); err != nil {
			return result, err
		}
	}
	if result.port, err = intOption(op, "port", result.port); err != nil {
		return result, err
	}
	if result.database, err = intOption(op, "db", 0); err != nil {
		return result, err
	}
	if result.tls, err = boolOption(op, "tls"); err != nil {
		return result, err
	}
	if result.port < 1 || result.port > 65535 {
		return result, fmt.Errorf("--port must be between 1 and 65535")
	}
	if result.database < 0 {
		return result, fmt.Errorf("--db must not be negative")
	}
	if (result.cacert != "" || result.cert != "" || result.key != "") && !result.tls {
		return result, fmt.Errorf("--cacert, --cert, and --key require --tls")
	}
	if (result.cert == "") != (result.key == "") {
		return result, fmt.Errorf("--cert and --key must be specified together")
	}
	return result, nil
}

func clientArgs(opts options) []string {
	args := []string{"--raw"}
	if opts.socket != "" {
		args = append(args, "-s", opts.socket)
	} else {
		args = append(args, "-h", opts.host, "-p", strconv.FormatInt(opts.port, 10))
	}
	if opts.database != 0 {
		args = append(args, "-n", strconv.FormatInt(opts.database, 10))
	}
	if opts.user != "" {
		args = append(args, "--user", opts.user)
	}
	if opts.tls {
		args = append(args, "--tls")
	}
	if opts.cacert != "" {
		args = append(args, "--cacert", opts.cacert)
	}
	if opts.cert != "" {
		args = append(args, "--cert", opts.cert, "--key", opts.key)
	}
	return args
}

func parseInfo(output []byte) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			values[strings.ToLower(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return values
}

func buildStatus(v map[string]string, opts options) Status {
	status := Status{
		Instance: instanceName(opts), Version: v["redis_version"], Role: v["role"], UptimeSeconds: uintValue(v, "uptime_in_seconds"),
		MemoryBytes: uintValue(v, "used_memory"), RSSBytes: uintValue(v, "used_memory_rss"), MaxMemoryBytes: uintValue(v, "maxmemory"),
		MaxMemoryPolicy: v["maxmemory_policy"], FragmentationRatio: floatValue(v, "mem_fragmentation_ratio"),
		Connections: uintValue(v, "connected_clients"), BlockedClients: uintValue(v, "blocked_clients"), MaxClients: uintValue(v, "maxclients"),
		TotalConnections: uintValue(v, "total_connections_received"), RejectedConnections: uintValue(v, "rejected_connections"),
		OperationsPerSecond: uintValue(v, "instantaneous_ops_per_sec"), EvictedKeys: uintValue(v, "evicted_keys"), ExpiredKeys: uintValue(v, "expired_keys"),
	}
	if status.MaxMemoryBytes > 0 {
		status.MaxMemoryUsedPercent = percent(status.MemoryBytes, status.MaxMemoryBytes)
	}
	hits, misses := uintValue(v, "keyspace_hits"), uintValue(v, "keyspace_misses")
	if hits+misses > 0 {
		status.HitRatePercent = percent(hits, hits+misses)
	}
	for key, value := range v {
		if strings.HasPrefix(key, "db") {
			status.Keys += redisDBKeys(value)
		}
	}
	return status
}

func redisDBKeys(value string) uint64 {
	for _, field := range strings.Split(value, ",") {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) == 2 && parts[0] == "keys" {
			number, _ := strconv.ParseUint(parts[1], 10, 64)
			return number
		}
	}
	return 0
}

func parseConfigUint(output []byte, name string) uint64 {
	lines := strings.Fields(string(output))
	for index := 0; index+1 < len(lines); index++ {
		if strings.EqualFold(lines[index], name) {
			value, _ := strconv.ParseUint(lines[index+1], 10, 64)
			return value
		}
	}
	return 0
}

func isRedisError(output []byte) bool {
	text := strings.TrimSpace(string(output))
	upper := strings.ToUpper(text)
	return strings.HasPrefix(upper, "ERR ") || strings.HasPrefix(upper, "NOAUTH ") || strings.HasPrefix(upper, "WRONGPASS ") || strings.HasPrefix(text, "(error)")
}

func isRedisAuthenticationError(output []byte) bool {
	upper := strings.ToUpper(strings.TrimSpace(string(output)))
	return strings.HasPrefix(upper, "NOAUTH ") || strings.HasPrefix(upper, "WRONGPASS ")
}

func instanceName(opts options) string {
	if opts.socket != "" {
		return opts.socket
	}
	return fmt.Sprintf("%s:%d", opts.host, opts.port)
}
func uintValue(values map[string]string, key string) uint64 {
	value, _ := strconv.ParseUint(values[key], 10, 64)
	return value
}
func floatValue(values map[string]string, key string) float64 {
	value, _ := strconv.ParseFloat(values[key], 64)
	return value
}
func percent(part, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) * 100 / float64(total)
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
func boolOption(op v1alpha1.Operation, name string) (bool, error) {
	raw, ok := op.Options[name]
	if !ok {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("invalid --%s: %w", name, err)
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
	combined := strings.ToUpper(string(result.Stdout) + "\n" + string(result.Stderr))
	if strings.Contains(combined, "NOAUTH") || strings.Contains(combined, "WRONGPASS") {
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
