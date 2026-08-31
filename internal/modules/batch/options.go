package batch

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
)

func stringOption(op v1alpha1.Operation, name string) (string, error) {
	var value string
	if raw, ok := op.Options[name]; ok {
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", fmt.Errorf("invalid --%s: %w", name, err)
		}
	}
	return value, nil
}

func stringsOption(op v1alpha1.Operation, name string) ([]string, error) {
	value := []string{}
	if raw, ok := op.Options[name]; ok {
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("invalid --%s: %w", name, err)
		}
	}
	return value, nil
}

func intOption(op v1alpha1.Operation, name string) (int, error) {
	var value int64
	if raw, ok := op.Options[name]; ok {
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, fmt.Errorf("invalid --%s: %w", name, err)
		}
	}
	return int(value), nil
}

func durationOption(op v1alpha1.Operation, name string) (time.Duration, error) {
	value, err := intOption(op, name)
	return time.Duration(value) * time.Millisecond, err
}

func boolOption(op v1alpha1.Operation, name string) (bool, error) {
	var value bool
	if raw, ok := op.Options[name]; ok {
		if err := json.Unmarshal(raw, &value); err != nil {
			return false, fmt.Errorf("invalid --%s: %w", name, err)
		}
	}
	return value, nil
}

func validateExecutionOptions(concurrency int, timeout time.Duration, arguments []string) error {
	if concurrency < 1 || concurrency > 128 {
		return fmt.Errorf("concurrency must be between 1 and 128")
	}
	if timeout < 100*time.Millisecond || timeout > 24*time.Hour {
		return fmt.Errorf("command timeout must be between 100ms and 24h")
	}
	if len(arguments) == 0 || strings.TrimSpace(arguments[0]) == "" {
		return fmt.Errorf("command is required after --")
	}
	if len(arguments) > 256 {
		return fmt.Errorf("command has more than 256 arguments")
	}
	totalBytes := 0
	for _, argument := range arguments {
		if strings.IndexByte(argument, 0) >= 0 {
			return fmt.Errorf("command arguments cannot contain NUL bytes")
		}
		totalBytes += len(argument)
	}
	if totalBytes > 64<<10 {
		return fmt.Errorf("command arguments exceed 64 KiB")
	}
	return nil
}

func outputLimitOption(op v1alpha1.Operation) (int64, error) {
	value, err := intOption(op, "max-output")
	if err != nil {
		return 0, err
	}
	if value == 0 {
		value = defaultCommandOutput
	}
	if value < minCommandOutput || value > maxCommandOutput {
		return 0, fmt.Errorf("max output must be between %d and %d bytes", minCommandOutput, maxCommandOutput)
	}
	return int64(value), nil
}
