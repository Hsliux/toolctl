package core

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"time"

	"toolctl/api/v1alpha1"
)

type RunOutput struct {
	Items    []v1alpha1.Item
	Warnings []v1alpha1.Diagnostic
	Errors   []v1alpha1.TargetError
}

type Runner interface {
	Execute(context.Context, v1alpha1.Operation) (RunOutput, error)
}

// StreamingRunner emits complete snapshots as they become available. It is
// intentionally optional so ordinary modules keep the bounded Runner contract.
type StreamingRunner interface {
	ExecuteStream(context.Context, v1alpha1.Operation, func(RunOutput) error) error
}

type Module interface {
	Info() v1alpha1.ModuleInfo
	Capabilities() []v1alpha1.Capability
	NewRunner(Dependencies) (Runner, error)
	Doctor(context.Context) []v1alpha1.CheckResult
}

type ProbeStatus string

const (
	ProbeAvailable   ProbeStatus = "available"
	ProbeDegraded    ProbeStatus = "degraded"
	ProbeUnavailable ProbeStatus = "unavailable"
)

type ProbeResult struct {
	Status     ProbeStatus
	Reason     string
	Suggestion string
}

type Backend interface {
	Name() string
	Probe(context.Context) ProbeResult
	Supports(capabilityID string) bool
	Execute(context.Context, v1alpha1.Operation) (RunOutput, error)
}

type FileSystemStats struct {
	TotalBytes     uint64
	FreeBytes      uint64
	AvailableBytes uint64
}

type FileSystem interface {
	ReadFile(path string, maxBytes int64) ([]byte, error)
	ReadDir(path string) ([]fs.DirEntry, error)
	Stat(path string) (fs.FileInfo, error)
	StatFS(path string) (FileSystemStats, error)
}

type ProcessSpec struct {
	Path      string
	Args      []string
	Env       []string
	Dir       string
	Stdin     io.Reader
	MaxStdout int64
	MaxStderr int64
}

type ProcessResult struct {
	ExitCode       int
	Stdout         []byte
	Stderr         []byte
	Duration       time.Duration
	TimedOut       bool
	StdoutExceeded bool
	StderrExceeded bool
	Err            error
}

type ProcessExecutor interface {
	LookPath(string) (string, error)
	Run(context.Context, ProcessSpec) ProcessResult
}

type Environment interface {
	Lookup(key string) (string, bool)
	Allowed(keys ...string) []string
}

type Clock interface {
	Now() time.Time
}

type Waiter interface {
	Wait(context.Context, time.Duration) error
}

type IDGenerator interface {
	NewID() string
}

type SecretResolver interface {
	Resolve(context.Context, string) ([]byte, error)
}

type Dependencies struct {
	Files                 FileSystem
	Processes             ProcessExecutor
	Environment           Environment
	Secrets               SecretResolver
	Clock                 Clock
	Waiter                Waiter
	IDs                   IDGenerator
	Logger                *slog.Logger
	Hostname              func() (string, error)
	Architecture          string
	OperatingSystem       string
	LogicalCPUs           int
	ConfigDirectory       string
	ConfigScope           string
	SystemConfigDirectory string
}
