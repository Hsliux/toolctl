package executor

import (
	"context"
	"fmt"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/apperror"
	"toolctl/internal/core"
	"toolctl/internal/registry"
	resultbuilder "toolctl/internal/result"
)

type Executor struct {
	registry *registry.Registry
	clock    core.Clock
}

// ExecuteStream renders each emitted output as an independent Result snapshot.
// Context cancellation is a normal end condition for an interactive stream.
func (e *Executor) ExecuteStream(parent context.Context, op v1alpha1.Operation, emit func(v1alpha1.Result) error) (v1alpha1.Result, bool, error) {
	registration, ok := e.registry.Resolve(op.Capability)
	if !ok {
		err := apperror.New(v1alpha1.ErrorUnsupportedCapability, "capability is not registered: "+op.Capability)
		result := resultbuilder.Build(op, core.RunOutput{}, err, v1alpha1.ModuleInfo{}, e.clock.Now(), 0)
		if emitErr := emit(result); emitErr != nil {
			return result, true, emitErr
		}
		return result, true, nil
	}
	runner, ok := registration.Runner.(core.StreamingRunner)
	if !ok {
		err := apperror.New(v1alpha1.ErrorUnsupportedCapability, "capability does not support streaming: "+op.Capability)
		result := resultbuilder.Build(op, core.RunOutput{}, err, registration.Module, e.clock.Now(), 0)
		if emitErr := emit(result); emitErr != nil {
			return result, true, emitErr
		}
		return result, true, nil
	}
	var last v1alpha1.Result
	emitted := false
	streamErr := runner.ExecuteStream(parent, op, func(output core.RunOutput) error {
		started := e.clock.Now()
		last = resultbuilder.Build(op, output, nil, registration.Module, e.clock.Now(), e.clock.Now().Sub(started))
		emitted = true
		return emit(last)
	})
	if streamErr != nil && parent.Err() == nil {
		return last, emitted, fmt.Errorf("stream %s: %w", op.Capability, streamErr)
	}
	return last, emitted, nil
}

func New(registry *registry.Registry, clock core.Clock) *Executor {
	return &Executor{registry: registry, clock: clock}
}

func (e *Executor) Execute(parent context.Context, op v1alpha1.Operation) v1alpha1.Result {
	started := e.clock.Now()
	registration, ok := e.registry.Resolve(op.Capability)
	if !ok {
		err := apperror.New(v1alpha1.ErrorUnsupportedCapability, "capability is not registered: "+op.Capability)
		return resultbuilder.Build(op, core.RunOutput{}, err, v1alpha1.ModuleInfo{}, e.clock.Now(), e.clock.Now().Sub(started))
	}
	ctx := parent
	cancel := func() {}
	if op.TimeoutMS > 0 {
		ctx, cancel = context.WithTimeout(parent, time.Duration(op.TimeoutMS)*time.Millisecond)
	}
	defer cancel()
	output, err := registration.Runner.Execute(ctx, op)
	if ctx.Err() == context.DeadlineExceeded {
		err = apperror.New(v1alpha1.ErrorTimeout, "operation timed out")
	} else if ctx.Err() == context.Canceled {
		err = apperror.New(v1alpha1.ErrorCancelled, "operation cancelled")
	}
	finished := e.clock.Now()
	return resultbuilder.Build(op, output, err, registration.Module, finished, finished.Sub(started))
}

func ExitCode(result v1alpha1.Result) int {
	if result.Status == v1alpha1.StatusSuccess {
		return 0
	}
	for _, item := range result.Errors {
		switch item.Code {
		case v1alpha1.ErrorInvalidArgument, v1alpha1.ErrorConfig:
			return 2
		case v1alpha1.ErrorPluginNotFound, v1alpha1.ErrorPluginIncompatible, v1alpha1.ErrorPluginProtocol,
			v1alpha1.ErrorUnsupportedPlatform, v1alpha1.ErrorUnsupportedCapability, v1alpha1.ErrorDependencyMissing:
			return 3
		case v1alpha1.ErrorTimeout, v1alpha1.ErrorCancelled:
			return 4
		}
	}
	return 1
}
