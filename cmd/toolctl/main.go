package main

import (
	"context"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"toolctl/internal/app"
	"toolctl/internal/cli"
	"toolctl/internal/modules"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	application, err := app.New(cli.BuildInfo{
		Version: version, Commit: commit, BuildTime: buildTime, GoVersion: runtime.Version(),
	}, modules.Builtins(), os.Stdout, os.Stderr)
	if err != nil {
		_, _ = os.Stderr.WriteString("error: " + err.Error() + "\n")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(application.Run(ctx))
}
