package linux

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"time"

	"toolctl/internal/core"
)

type ProcessExecutor struct{}

func NewProcessExecutor() ProcessExecutor { return ProcessExecutor{} }

func (ProcessExecutor) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (ProcessExecutor) Run(ctx context.Context, spec core.ProcessSpec) core.ProcessResult {
	started := time.Now()
	commandContext, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(commandContext, spec.Path, spec.Args...)
	command.Env = spec.Env
	command.Dir = spec.Dir
	command.Stdin = spec.Stdin

	stdout := &limitedBuffer{limit: spec.MaxStdout, onExceeded: cancel}
	stderr := &limitedBuffer{limit: spec.MaxStderr, onExceeded: cancel}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	result := core.ProcessResult{
		ExitCode:       -1,
		Stdout:         stdout.Bytes(),
		Stderr:         stderr.Bytes(),
		Duration:       time.Since(started),
		TimedOut:       errors.Is(ctx.Err(), context.DeadlineExceeded),
		StdoutExceeded: stdout.exceeded,
		StderrExceeded: stderr.exceeded,
		Err:            err,
	}
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if stdout.exceeded || stderr.exceeded {
		result.Err = errors.New("process output exceeded configured limit")
	}
	return result
}

type limitedBuffer struct {
	buffer     bytes.Buffer
	limit      int64
	exceeded   bool
	onExceeded func()
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if b.limit <= 0 {
		return b.buffer.Write(data)
	}
	remaining := b.limit - int64(b.buffer.Len())
	if remaining <= 0 {
		b.markExceeded()
		return len(data), nil
	}
	toWrite := data
	if int64(len(toWrite)) > remaining {
		toWrite = toWrite[:remaining]
		b.markExceeded()
	}
	_, err := b.buffer.Write(toWrite)
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

func (b *limitedBuffer) markExceeded() {
	if b.exceeded {
		return
	}
	b.exceeded = true
	if b.onExceeded != nil {
		b.onExceeded()
	}
}

func (b *limitedBuffer) Bytes() []byte {
	return append([]byte(nil), b.buffer.Bytes()...)
}

var _ io.Writer = (*limitedBuffer)(nil)
