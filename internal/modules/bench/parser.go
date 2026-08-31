package bench

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func parseFIOJSON(data []byte) (DiskBenchmarkResult, error) {
	var document map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return DiskBenchmarkResult{}, err
	}
	result := DiskBenchmarkResult{FIOVersion: textValue(document["fio version"])}
	jobs, ok := document["jobs"].([]any)
	if !ok || len(jobs) == 0 {
		return DiskBenchmarkResult{}, fmt.Errorf("fio jobs are missing")
	}
	latencyWeight := float64(0)
	for _, raw := range jobs {
		job, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if number(job["error"]) != 0 {
			return DiskBenchmarkResult{}, fmt.Errorf("fio job %s returned error %.0f", textValue(job["jobname"]), number(job["error"]))
		}
		read := mapObject(job["read"])
		write := mapObject(job["write"])
		result.Read = addIO(result.Read, read)
		result.Write = addIO(result.Write, write)
		for _, direction := range []map[string]any{read, write} {
			weight := number(direction["total_ios"])
			if weight <= 0 {
				continue
			}
			latency := latencyObject(direction)
			result.Latency.AverageMicros += number(latency["mean"]) / 1000 * weight
			latencyWeight += weight
			result.Latency.MaximumMicros = math.Max(result.Latency.MaximumMicros, number(latency["max"])/1000)
			percentiles := mapObject(latency["percentile"])
			result.Latency.P50Micros = math.Max(result.Latency.P50Micros, percentile(percentiles, 50)/1000)
			result.Latency.P95Micros = math.Max(result.Latency.P95Micros, percentile(percentiles, 95)/1000)
			result.Latency.P99Micros = math.Max(result.Latency.P99Micros, percentile(percentiles, 99)/1000)
			result.Latency.P999Micros = math.Max(result.Latency.P999Micros, percentile(percentiles, 99.9)/1000)
		}
		result.UserCPUPercent += number(job["usr_cpu"])
		result.SystemCPUPercent += number(job["sys_cpu"])
		result.ContextSwitches += uint64(number(job["ctx"]))
	}
	if latencyWeight > 0 {
		result.Latency.AverageMicros /= latencyWeight
	}
	if disks, ok := document["disk_util"].([]any); ok {
		for _, raw := range disks {
			result.DiskUtilPercent = math.Max(result.DiskUtilPercent, number(mapObject(raw)["util"]))
		}
	}
	return result, nil
}

func addIO(current IOMetrics, value map[string]any) IOMetrics {
	current.IOPS += number(value["iops"])
	bw := number(value["bw_bytes"])
	if bw == 0 {
		bw = number(value["bw"]) * 1024
	}
	bytes := number(value["io_bytes"])
	if bytes == 0 {
		bytes = number(value["io_kbytes"]) * 1024
	}
	current.BytesPerSecond += uint64(bw)
	current.Bytes += uint64(bytes)
	return current
}

func latencyObject(direction map[string]any) map[string]any {
	for _, key := range []string{"clat_ns", "lat_ns"} {
		if value := mapObject(direction[key]); len(value) > 0 {
			return value
		}
	}
	for _, key := range []string{"clat_us", "lat_us"} {
		if value := mapObject(direction[key]); len(value) > 0 {
			scaled := map[string]any{}
			for name, raw := range value {
				if name == "percentile" {
					percentiles := map[string]any{}
					for p, v := range mapObject(raw) {
						percentiles[p] = number(v) * 1000
					}
					scaled[name] = percentiles
				} else {
					scaled[name] = number(raw) * 1000
				}
			}
			return scaled
		}
	}
	return map[string]any{}
}

func percentile(values map[string]any, requested float64) float64 {
	closest, distance := float64(0), math.MaxFloat64
	for key, value := range values {
		parsed, err := strconv.ParseFloat(key, 64)
		if err != nil {
			continue
		}
		delta := math.Abs(parsed - requested)
		if delta < distance {
			closest, distance = number(value), delta
		}
	}
	return closest
}

func mapObject(value any) map[string]any {
	result, _ := value.(map[string]any)
	if result == nil {
		return map[string]any{}
	}
	return result
}
func textValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
func number(value any) float64 {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := strconv.ParseFloat(typed.String(), 64)
		return parsed
	case float64:
		return typed
	case int:
		return float64(typed)
	case nil:
		return 0
	default:
		parsed, _ := strconv.ParseFloat(fmt.Sprint(typed), 64)
		return parsed
	}
}
