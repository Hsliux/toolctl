package batch

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

const maxHostFile = 1 << 20

func (r *Runner) executeSSH(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	hosts, err := stringsOption(op, "hosts")
	if err != nil {
		return invalidFailure(err), nil
	}
	hostFile, err := stringOption(op, "host-file")
	if err != nil {
		return invalidFailure(err), nil
	}
	if hostFile != "" {
		data, readErr := r.deps.Files.ReadFile(hostFile, maxHostFile)
		if readErr != nil {
			return invalidFailure(fmt.Errorf("read host file: %w", readErr)), nil
		}
		hosts = append(hosts, parseHostFile(string(data))...)
	}
	hosts, err = normalizeHosts(hosts)
	if err != nil {
		return invalidFailure(err), nil
	}
	if len(hosts) == 0 {
		return invalidFailure(fmt.Errorf("at least one --hosts or --host-file target is required")), nil
	}
	if len(hosts) > 1024 {
		return invalidFailure(fmt.Errorf("host count exceeds 1024")), nil
	}
	user, err := stringOption(op, "user")
	if err != nil {
		return invalidFailure(err), nil
	}
	if user != "" && !safeUser(user) {
		return invalidFailure(fmt.Errorf("invalid SSH user %q", user)), nil
	}
	port, err := intOption(op, "port")
	if err != nil || port < 0 || port > 65535 {
		return invalidFailure(fmt.Errorf("port must be between 1 and 65535 when specified")), nil
	}
	identity, err := stringOption(op, "identity")
	if err != nil {
		return invalidFailure(err), nil
	}
	concurrency, err := intOption(op, "jobs")
	if err != nil {
		return invalidFailure(err), nil
	}
	connectTimeout, err := durationOption(op, "connect-timeout")
	if err != nil || connectTimeout < time.Second || connectTimeout > 10*time.Minute {
		return invalidFailure(fmt.Errorf("connect timeout must be between 1s and 10m")), nil
	}
	commandTimeout, err := durationOption(op, "command-timeout")
	if err != nil {
		return invalidFailure(err), nil
	}
	if err := validateExecutionOptions(concurrency, commandTimeout, op.Arguments); err != nil {
		return invalidFailure(err), nil
	}
	outputLimit, err := outputLimitOption(op)
	if err != nil {
		return invalidFailure(err), nil
	}
	acceptNew, err := boolOption(op, "accept-new")
	if err != nil {
		return invalidFailure(err), nil
	}
	sshPath, err := r.deps.Processes.LookPath("ssh")
	if err != nil {
		return dependencyFailure("ssh", err), nil
	}
	targets := make([]commandTarget, 0, len(hosts))
	for _, host := range hosts {
		destination := host
		if user != "" {
			destination = user + "@" + host
		}
		args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=" + strconv.Itoa(int(connectTimeout/time.Second))}
		if port > 0 {
			args = append(args, "-p", strconv.Itoa(port))
		}
		if acceptNew {
			args = append(args, "-o", "StrictHostKeyChecking=accept-new")
		}
		if identity != "" {
			args = append(args, "-i", identity)
		}
		args = append(args, "--", destination)
		args = append(args, op.Arguments...)
		targets = append(targets, commandTarget{Name: host, Path: sshPath, Args: args})
	}
	sortTargets(targets)
	return r.runTargets(ctx, targets, concurrency, commandTimeout, outputLimit), nil
}

func parseHostFile(content string) []string {
	hosts := []string{}
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if before, _, found := strings.Cut(line, "#"); found {
			line = strings.TrimSpace(before)
		}
		if line == "" {
			continue
		}
		for _, field := range strings.Fields(line) {
			hosts = append(hosts, field)
		}
	}
	return hosts
}

func normalizeHosts(values []string) ([]string, error) {
	result := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !safeHost(value) {
			return nil, fmt.Errorf("invalid host %q", value)
		}
		if !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result, nil
}

func safeHost(value string) bool {
	if value == "" || len(value) > 253 || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "@/\\[]% ") {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	// A trailing dot is valid for an absolute DNS name. Underscores are not
	// valid RFC host-label characters, but OpenSSH commonly uses them in Host
	// aliases, which are safe here because the destination is passed after --.
	name := strings.TrimSuffix(value, ".")
	if name == "" {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_') {
				return false
			}
		}
	}
	return true
}

func safeUser(value string) bool {
	if value == "" || len(value) > 64 || strings.HasPrefix(value, "-") {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("_.-", character)) {
			return false
		}
	}
	return true
}
