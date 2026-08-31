package assess

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

type MetricComparison struct {
	Metric        string  `json:"metric"`
	Baseline      float64 `json:"baseline"`
	Current       float64 `json:"current"`
	ChangePercent float64 `json:"changePercent"`
	Status        string  `json:"status"`
}

func (r *Runner) compare(op v1alpha1.Operation) (core.RunOutput, error) {
	if r.files == nil {
		return assessmentFailure(v1alpha1.ErrorExecutionFailed, "filesystem dependency is unavailable"), nil
	}
	baselinePath, err := requiredStringOption(op, "baseline")
	if err != nil {
		return assessmentFailure(v1alpha1.ErrorInvalidArgument, err.Error()), nil
	}
	currentPath, err := requiredStringOption(op, "current")
	if err != nil {
		return assessmentFailure(v1alpha1.ErrorInvalidArgument, err.Error()), nil
	}
	warn, err := integerOption(op, "warn-percent", 10)
	if err != nil {
		return assessmentFailure(v1alpha1.ErrorInvalidArgument, err.Error()), nil
	}
	fail, err := integerOption(op, "fail-percent", 25)
	if err != nil {
		return assessmentFailure(v1alpha1.ErrorInvalidArgument, err.Error()), nil
	}
	if warn < 0 || fail <= warn || fail > 1000 {
		return assessmentFailure(v1alpha1.ErrorInvalidArgument, "comparison thresholds require 0 <= warn-percent < fail-percent <= 1000"), nil
	}
	baseline, err := readAssessmentMetrics(r.files, baselinePath)
	if err != nil {
		return assessmentFailure(v1alpha1.ErrorParseFailed, "baseline assessment could not be read: "+err.Error()), nil
	}
	current, err := readAssessmentMetrics(r.files, currentPath)
	if err != nil {
		return assessmentFailure(v1alpha1.ErrorParseFailed, "current assessment could not be read: "+err.Error()), nil
	}
	keys := []string{}
	for key := range baseline {
		if _, ok := current[key]; ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	output := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
	for _, key := range keys {
		base, now := baseline[key], current[key]
		change := float64(0)
		if base != 0 {
			change = (now - base) / math.Abs(base) * 100
		} else if now != 0 {
			change = 100
		}
		status := "pass"
		regression := metricRegression(key, base, now)
		if regression >= float64(fail) {
			status = "fail"
		} else if regression >= float64(warn) {
			status = "warn"
		}
		comparison := MetricComparison{key, base, now, change, status}
		item, e := resultbuilder.NewItem("MetricComparison", key, "", comparison)
		if e != nil {
			return core.RunOutput{}, e
		}
		output.Items = append(output.Items, item)
		if status == "warn" {
			output.Warnings = append(output.Warnings, v1alpha1.Diagnostic{Code: "METRIC_DRIFT", Message: "assessment metric changed beyond warning threshold", Details: map[string]string{"metric": key}})
		} else if status == "fail" {
			output.Errors = append(output.Errors, v1alpha1.TargetError{Target: key, Code: v1alpha1.ErrorExecutionFailed, Message: "assessment metric changed beyond failure threshold", Details: map[string]string{"metric": key}})
		}
	}
	if len(keys) == 0 {
		output.Warnings = append(output.Warnings, v1alpha1.Diagnostic{Code: "NO_COMPARABLE_METRICS", Message: "assessment files contain no matching benchmark metrics", Details: map[string]string{}})
	}
	return output, nil
}

func readAssessmentMetrics(files core.FileSystem, name string) (map[string]float64, error) {
	data, err := files.ReadFile(name, 16<<20)
	if err != nil {
		return nil, err
	}
	var result v1alpha1.Result
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	metrics := map[string]float64{}
	for _, outer := range result.Items {
		if outer.Kind != "AssessmentSection" {
			continue
		}
		var section Section
		if err := json.Unmarshal(outer.Data, &section); err != nil {
			continue
		}
		for _, item := range section.Items {
			var value any
			if json.Unmarshal(item.Data, &value) != nil {
				continue
			}
			flattenMetrics(metrics, section.Section+"/"+item.Kind+"/"+item.Name, "", value)
		}
	}
	return metrics, nil
}

func flattenMetrics(output map[string]float64, prefix, path string, value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			next := key
			if path != "" {
				next = path + "." + key
			}
			flattenMetrics(output, prefix, next, child)
		}
	case float64:
		if comparableMetric(path) {
			output[prefix+"/"+path] = typed
		}
	}
}
func comparableMetric(path string) bool {
	for _, suffix := range []string{"hashesPerSecond", "bytesPerSecond", "copyBytesPerSecond", "randomReadsPerSecond", "randomReadLatencyNs", "iops", "averageMicros", "p95Micros", "p99Micros", "p999Micros", "sentBytesPerSecond", "receivedBytesPerSecond"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func metricRegression(metric string, baseline, current float64) float64 {
	if baseline == 0 {
		if current == 0 {
			return 0
		}
		return 0
	}
	lowerIsBetter := strings.HasSuffix(metric, "LatencyNs") || strings.HasSuffix(metric, "Micros")
	if lowerIsBetter {
		return (current - baseline) / math.Abs(baseline) * 100
	}
	return (baseline - current) / math.Abs(baseline) * 100
}
func requiredStringOption(op v1alpha1.Operation, name string) (string, error) {
	value, err := stringOption(op, name)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("--%s is required", name)
	}
	return value, nil
}
func integerOption(op v1alpha1.Operation, name string, fallback int64) (int64, error) {
	value := fallback
	if raw, ok := op.Options[name]; ok {
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, fmt.Errorf("invalid --%s: %w", name, err)
		}
	}
	return value, nil
}
