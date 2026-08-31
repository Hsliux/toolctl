package result

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/apperror"
	"toolctl/internal/core"
)

func NewItem(kind, name, target string, value any) (v1alpha1.Item, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return v1alpha1.Item{}, fmt.Errorf("marshal %s item: %w", kind, err)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return v1alpha1.Item{}, fmt.Errorf("item data for %s must be a JSON object", kind)
	}
	return v1alpha1.Item{Target: target, Name: name, Kind: kind, Data: data}, nil
}

func Build(op v1alpha1.Operation, output core.RunOutput, runErr error, module v1alpha1.ModuleInfo, generatedAt time.Time, duration time.Duration) v1alpha1.Result {
	items := append([]v1alpha1.Item(nil), output.Items...)
	warnings := append([]v1alpha1.Diagnostic(nil), output.Warnings...)
	errorsOut := append([]v1alpha1.TargetError(nil), output.Errors...)
	if runErr != nil {
		errorsOut = append(errorsOut, apperror.TargetError(runErr))
	}
	if items == nil {
		items = []v1alpha1.Item{}
	}
	if warnings == nil {
		warnings = []v1alpha1.Diagnostic{}
	}
	if errorsOut == nil {
		errorsOut = []v1alpha1.TargetError{}
	}
	status := v1alpha1.StatusSuccess
	if len(errorsOut) > 0 && len(items) > 0 {
		status = targetAwareFailureStatus(items, errorsOut)
	} else if len(errorsOut) > 0 {
		status = v1alpha1.StatusFailure
	}
	return v1alpha1.Result{
		APIVersion: v1alpha1.APIVersion,
		Kind:       "Result",
		Status:     status,
		Operation: v1alpha1.OperationRef{
			ID:         op.ID,
			Capability: op.Capability,
		},
		Items:    items,
		Warnings: warnings,
		Errors:   errorsOut,
		Metadata: v1alpha1.ResultMetadata{
			GeneratedAt: generatedAt.UTC(),
			DurationMS:  duration.Milliseconds(),
			Module: v1alpha1.ModuleRef{
				Name:    module.Name,
				Version: module.Version,
			},
		},
	}
}

func targetAwareFailureStatus(items []v1alpha1.Item, errorsOut []v1alpha1.TargetError) v1alpha1.ResultStatus {
	failedTargets := map[string]bool{}
	for _, item := range errorsOut {
		if item.Target != "" {
			failedTargets[item.Target] = true
		}
	}
	if len(failedTargets) == 0 {
		return v1alpha1.StatusPartial
	}
	for _, item := range items {
		if item.Target == "" || !failedTargets[item.Target] {
			return v1alpha1.StatusPartial
		}
	}
	return v1alpha1.StatusFailure
}
