package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/apperror"
	"toolctl/internal/cli"
	"toolctl/internal/core"
	"toolctl/internal/executor"
	"toolctl/internal/platform/linux"
	"toolctl/internal/registry"
	"toolctl/internal/render"
)

type App struct {
	cli    *cli.CLI
	stderr io.Writer
}

func New(info cli.BuildInfo, modules []core.Module, stdout, stderr io.Writer) (*App, error) {
	clock := systemClock{}
	ids := randomIDs{}
	configDirectory := ""
	configScope := "user"
	systemConfigDirectory := ""
	if directory, err := os.UserConfigDir(); err == nil {
		configDirectory = directory + string(os.PathSeparator) + "toolctl"
	}
	if runtime.GOOS == "linux" {
		systemConfigDirectory = "/etc/toolctl"
	}
	if directory := os.Getenv("TOOLCTL_CONFIG_DIR"); directory != "" {
		configDirectory = directory
		configScope = "explicit"
		systemConfigDirectory = ""
	}
	deps := core.Dependencies{
		Files:                 linux.NewFileSystem(),
		Processes:             linux.NewProcessExecutor(),
		Clock:                 clock,
		Waiter:                systemWaiter{},
		IDs:                   ids,
		Logger:                slog.New(slog.NewTextHandler(stderr, nil)),
		Hostname:              os.Hostname,
		Architecture:          runtime.GOARCH,
		OperatingSystem:       runtime.GOOS,
		LogicalCPUs:           runtime.NumCPU(),
		ConfigDirectory:       configDirectory,
		ConfigScope:           configScope,
		SystemConfigDirectory: systemConfigDirectory,
	}
	moduleRegistry, err := registry.New(modules, deps)
	if err != nil {
		return nil, err
	}
	operationExecutor := executor.New(moduleRegistry, clock)
	command, err := cli.New(info, moduleRegistry.Capabilities(), ids, func(ctx context.Context, op v1alpha1.Operation, options render.Options) (int, error) {
		if operationBool(op, "live") || operationBool(op, "service") {
			if options.Format != render.FormatTable && options.Format != render.FormatWide {
				return 2, apperror.New(v1alpha1.ErrorInvalidArgument, "--live and --service support table and wide output")
			}
			first := true
			last, emitted, streamErr := operationExecutor.ExecuteStream(ctx, op, func(snapshot v1alpha1.Result) error {
				streamOptions := options
				streamOptions.NoHeaders = options.NoHeaders || !first
				first = false
				if err := render.Write(stdout, snapshot, streamOptions); err != nil {
					return err
				}
				writeDiagnostics(stderr, snapshot)
				return nil
			})
			if streamErr != nil {
				return 1, apperror.Wrap(v1alpha1.ErrorInternal, "render live stream", streamErr)
			}
			if !emitted {
				return 0, nil
			}
			return executor.ExitCode(last), nil
		}
		result := operationExecutor.Execute(ctx, op)
		if err := render.Write(stdout, result, options); err != nil {
			return 1, apperror.Wrap(v1alpha1.ErrorInternal, "render result", err)
		}
		if options.Format == render.FormatTable || options.Format == render.FormatWide {
			writeDiagnostics(stderr, result)
		}
		return executor.ExitCode(result), nil
	}, stdout, stderr)
	if err != nil {
		return nil, err
	}
	return &App{cli: command, stderr: stderr}, nil
}

func operationBool(op v1alpha1.Operation, name string) bool {
	raw, ok := op.Options[name]
	if !ok {
		return false
	}
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}

func writeDiagnostics(writer io.Writer, result v1alpha1.Result) {
	for _, warning := range result.Warnings {
		_, _ = fmt.Fprintf(writer, "warning: %s: %s\n", warning.Code, warning.Message)
	}
	for _, item := range result.Errors {
		_, _ = fmt.Fprintf(writer, "error: %s: %s\n", item.Code, item.Message)
	}
}

func (a *App) Run(ctx context.Context) int {
	exitCode, err := a.cli.Execute(ctx)
	if err == nil {
		return exitCode
	}
	if !cli.ErrorReported(err) {
		_, _ = fmt.Fprintf(a.stderr, "error: %s\n", err)
	}
	switch apperror.Code(err) {
	case v1alpha1.ErrorInvalidArgument, v1alpha1.ErrorConfig:
		return 2
	case v1alpha1.ErrorPluginNotFound, v1alpha1.ErrorPluginIncompatible, v1alpha1.ErrorPluginProtocol,
		v1alpha1.ErrorUnsupportedPlatform, v1alpha1.ErrorUnsupportedCapability, v1alpha1.ErrorDependencyMissing:
		return 3
	case v1alpha1.ErrorTimeout, v1alpha1.ErrorCancelled:
		return 4
	default:
		return 1
	}
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type systemWaiter struct{}

func (systemWaiter) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type randomIDs struct{}

func (randomIDs) NewID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("op-%d", time.Now().UnixNano())
	}
	return "op-" + hex.EncodeToString(buffer)
}
