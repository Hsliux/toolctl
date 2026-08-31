package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

const cpuBlockSize = 1 << 20

type memoryWorker struct{ source, destination []byte }

func (r *Runner) runCPU(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	duration, threads, err := benchmarkRuntimeOptions(op, r.deps.LogicalCPUs, 10*time.Second, 0)
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	if op.TimeoutMS > 0 && duration+time.Second >= time.Duration(op.TimeoutMS)*time.Millisecond {
		return failure(v1alpha1.ErrorInvalidArgument, "operation timeout is too short for CPU benchmark; increase --timeout", nil), nil
	}
	workCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	started := time.Now()
	type workerResult struct {
		hashes uint64
		sum    [sha256.Size]byte
	}
	results := make(chan workerResult, threads)
	var group sync.WaitGroup
	for worker := 0; worker < threads; worker++ {
		group.Add(1)
		go func(id int) {
			defer group.Done()
			block := make([]byte, cpuBlockSize)
			for index := range block {
				block[index] = byte(index + id)
			}
			count := uint64(0)
			var sum [sha256.Size]byte
			for {
				select {
				case <-workCtx.Done():
					results <- workerResult{hashes: count, sum: sum}
					return
				default:
					sum = sha256.Sum256(block)
					block[count%cpuBlockSize] ^= sum[count%sha256.Size]
					count++
				}
			}
		}(worker)
	}
	group.Wait()
	close(results)
	if ctx.Err() != nil {
		return core.RunOutput{}, ctx.Err()
	}
	elapsed := time.Since(started)
	hashes := uint64(0)
	combined := make([]byte, sha256.Size)
	for result := range results {
		hashes += result.hashes
		for index := range combined {
			combined[index] ^= result.sum[index]
		}
	}
	seconds := elapsed.Seconds()
	bytesProcessed := hashes * cpuBlockSize
	result := CPUBenchmarkResult{Algorithm: "sha256-1m", Threads: threads, DurationMS: elapsed.Milliseconds(), Hashes: hashes, HashesPerSecond: float64(hashes) / seconds, BytesProcessed: bytesProcessed, BytesPerSecond: uint64(float64(bytesProcessed) / seconds), Checksum: hex.EncodeToString(combined)}
	item, err := resultbuilder.NewItem("CPUBenchmarkResult", "cpu", "", result)
	if err != nil {
		return core.RunOutput{}, err
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
}

func (r *Runner) runMemory(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	duration, threads, err := benchmarkRuntimeOptions(op, r.deps.LogicalCPUs, 5*time.Second, 1)
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	sizeText, err := benchmarkStringOption(op, "size", "64M")
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	size, err := parseSize(sizeText)
	if err != nil || size < 8<<20 || size > 1<<30 {
		return failure(v1alpha1.ErrorInvalidArgument, "memory size must be between 8M and 1G", err), nil
	}
	if size/uint64(threads) < 1<<20 {
		return failure(v1alpha1.ErrorInvalidArgument, "memory size must provide at least 1M per thread", nil), nil
	}
	if op.TimeoutMS > 0 && 2*duration+time.Second >= time.Duration(op.TimeoutMS)*time.Millisecond {
		return failure(v1alpha1.ErrorInvalidArgument, "operation timeout is too short for both memory benchmark phases; increase --timeout", nil), nil
	}
	started := time.Now()
	perWorker := int(size / uint64(threads))
	workers := make([]memoryWorker, threads)
	for index := range workers {
		workers[index].source = make([]byte, perWorker)
		workers[index].destination = make([]byte, perWorker)
		for offset := range workers[index].source {
			workers[index].source[offset] = byte(offset + index)
		}
	}
	copyCount, copyChecksum := runMemoryPhase(ctx, duration, workers, true)
	if ctx.Err() != nil {
		return core.RunOutput{}, ctx.Err()
	}
	randomCount, randomChecksum := runMemoryPhase(ctx, duration, workers, false)
	elapsed := time.Since(started)
	copyBytes := copyCount * uint64(perWorker)
	randomLatency := float64(0)
	if randomCount > 0 {
		// randomCount is aggregated across all workers. Scale it back to an
		// approximate per-worker access latency instead of reporting the
		// reciprocal of aggregate throughput as latency.
		randomLatency = duration.Seconds() * 1e9 * float64(threads) / float64(randomCount)
	}
	result := MemoryBenchmarkResult{
		Threads: threads, SizeBytes: uint64(perWorker * threads), PhaseDurationMS: duration.Milliseconds(), DurationMS: elapsed.Milliseconds(),
		CopyBytes: copyBytes, CopyBytesPerSecond: uint64(float64(copyBytes) / duration.Seconds()), RandomReads: randomCount,
		RandomReadsPerSecond: float64(randomCount) / duration.Seconds(), RandomReadLatencyNs: randomLatency, Checksum: copyChecksum ^ randomChecksum,
	}
	item, err := resultbuilder.NewItem("MemoryBenchmarkResult", "memory", "", result)
	if err != nil {
		return core.RunOutput{}, err
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
}

func runMemoryPhase(parent context.Context, duration time.Duration, workers []memoryWorker, copying bool) (uint64, uint64) {
	ctx, cancel := context.WithTimeout(parent, duration)
	defer cancel()
	type phaseResult struct{ count, checksum uint64 }
	results := make(chan phaseResult, len(workers))
	var group sync.WaitGroup
	for _, worker := range workers {
		worker := worker
		group.Add(1)
		go func() {
			defer group.Done()
			count, checksum, state := uint64(0), uint64(0), uint64(0x9e3779b97f4a7c15)
			for {
				select {
				case <-ctx.Done():
					results <- phaseResult{count: count, checksum: checksum}
					return
				default:
					if copying {
						copy(worker.destination, worker.source)
						checksum += uint64(worker.destination[count%uint64(len(worker.destination))])
					} else {
						state ^= state << 13
						state ^= state >> 7
						state ^= state << 17
						checksum += uint64(worker.source[state%uint64(len(worker.source))])
					}
					count++
				}
			}
		}()
	}
	group.Wait()
	close(results)
	total, checksum := uint64(0), uint64(0)
	for result := range results {
		total += result.count
		checksum ^= result.checksum
	}
	return total, checksum
}

func benchmarkRuntimeOptions(op v1alpha1.Operation, logicalCPUs int, defaultDuration time.Duration, defaultThreads int64) (time.Duration, int, error) {
	durationMS := defaultDuration.Milliseconds()
	if raw, ok := op.Options["duration"]; ok {
		if err := json.Unmarshal(raw, &durationMS); err != nil {
			return 0, 0, fmt.Errorf("invalid --duration: %w", err)
		}
	}
	threads := defaultThreads
	if raw, ok := op.Options["threads"]; ok {
		if err := json.Unmarshal(raw, &threads); err != nil {
			return 0, 0, fmt.Errorf("invalid --threads: %w", err)
		}
	}
	if logicalCPUs <= 0 {
		logicalCPUs = runtime.NumCPU()
	}
	if threads == 0 {
		threads = int64(logicalCPUs)
	}
	duration := time.Duration(durationMS) * time.Millisecond
	if duration < 100*time.Millisecond || duration > time.Hour {
		return 0, 0, fmt.Errorf("duration must be between 100ms and 1h")
	}
	if threads < 1 || threads > int64(logicalCPUs) || threads > 256 {
		return 0, 0, fmt.Errorf("threads must be between 1 and %d", minInt(logicalCPUs, 256))
	}
	return duration, int(threads), nil
}

func benchmarkStringOption(op v1alpha1.Operation, name, defaultValue string) (string, error) {
	raw, ok := op.Options[name]
	if !ok {
		return defaultValue, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("invalid --%s: %w", name, err)
	}
	return strings.TrimSpace(value), nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
