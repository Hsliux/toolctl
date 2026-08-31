package linux

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"toolctl/internal/core"
)

func TestProcessExecutorLimitsOutput(t *testing.T) {
	result := runHelper(t, context.Background(), "output", 4)
	if result.Err == nil || string(result.Stdout) != "1234" || !result.StdoutExceeded || result.StderrExceeded {
		t.Fatalf("result = %#v", result)
	}
}

func TestProcessExecutorReportsTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := runHelper(t, ctx, "sleep", 1024)
	if !result.TimedOut || result.Err == nil {
		t.Fatalf("result = %#v", result)
	}
}

func runHelper(t *testing.T, ctx context.Context, action string, limit int64) core.ProcessResult {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return NewProcessExecutor().Run(ctx, core.ProcessSpec{
		Path:      executable,
		Args:      []string{"-test.run=TestProcessHelper", "--", action},
		Env:       []string{"GO_WANT_TOOLCTL_PROCESS_HELPER=1"},
		MaxStdout: limit,
		MaxStderr: 1024,
	})
}

func TestProcessHelper(t *testing.T) {
	if os.Getenv("GO_WANT_TOOLCTL_PROCESS_HELPER") != "1" {
		return
	}
	action := os.Args[len(os.Args)-1]
	switch action {
	case "output":
		_, _ = fmt.Fprint(os.Stdout, "123456789")
	case "sleep":
		time.Sleep(5 * time.Second)
	}
	os.Exit(0)
}
