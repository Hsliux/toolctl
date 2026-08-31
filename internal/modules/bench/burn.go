package bench

import (
	"context"
	"crypto/sha256"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

func (r *Runner) runBurn(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	mode := strings.ToLower(strings.TrimSpace(op.Name))
	if mode == "" {
		mode = "mixed"
	}
	if mode != "cpu" && mode != "mem" && mode != "mixed" {
		return failure(v1alpha1.ErrorInvalidArgument, "burn mode must be cpu, mem, or mixed", nil), nil
	}
	duration, threads, err := benchmarkRuntimeOptions(op, r.deps.LogicalCPUs, 20*time.Second, 0)
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	if op.TimeoutMS > 0 && duration+time.Second >= time.Duration(op.TimeoutMS)*time.Millisecond {
		return failure(v1alpha1.ErrorInvalidArgument, "operation timeout is too short for stability workload; increase --timeout", nil), nil
	}
	sizeText, err := benchmarkStringOption(op, "size", "256M")
	if err != nil {
		return failure(v1alpha1.ErrorInvalidArgument, err.Error(), err), nil
	}
	size, err := parseSize(sizeText)
	if err != nil || size < 8<<20 || size > 1<<30 {
		return failure(v1alpha1.ErrorInvalidArgument, "memory size must be between 8M and 1G", err), nil
	}
	if mode == "cpu" {
		size = 0
	}

	workCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	started := time.Now()
	var hashes, memoryBytes, checksum atomic.Uint64
	var group sync.WaitGroup
	if mode == "cpu" || mode == "mixed" {
		for worker := 0; worker < threads; worker++ {
			group.Add(1)
			go burnCPUWorker(workCtx, worker, &hashes, &checksum, &group)
		}
	} else {
		threads = 0
	}
	if mode == "mem" || mode == "mixed" {
		group.Add(1)
		go burnMemoryWorker(workCtx, size, &memoryBytes, &checksum, &group)
	}
	group.Wait()
	if ctx.Err() != nil {
		return core.RunOutput{}, ctx.Err()
	}
	elapsed := time.Since(started)
	seconds := elapsed.Seconds()
	result := BurnResult{
		Mode: mode, Threads: threads, SizeBytes: size, DurationMS: elapsed.Milliseconds(),
		Hashes: hashes.Load(), HashesPerSecond: float64(hashes.Load()) / seconds,
		MemoryBytes: memoryBytes.Load(), MemoryBytesPerSecond: uint64(float64(memoryBytes.Load()) / seconds), Checksum: checksum.Load(),
	}
	item, err := resultbuilder.NewItem("BurnResult", mode, "", result)
	if err != nil {
		return core.RunOutput{}, err
	}
	return core.RunOutput{Items: []v1alpha1.Item{item}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
}

func burnCPUWorker(ctx context.Context, id int, hashes, checksum *atomic.Uint64, group *sync.WaitGroup) {
	defer group.Done()
	block := make([]byte, cpuBlockSize)
	for index := range block {
		block[index] = byte(index + id)
	}
	count, localChecksum := uint64(0), uint64(0)
	for ctx.Err() == nil {
		sum := sha256.Sum256(block)
		block[count%cpuBlockSize] ^= sum[count%sha256.Size]
		localChecksum ^= uint64(sum[0])<<56 | uint64(sum[31])
		count++
	}
	hashes.Add(count)
	checksum.Add(localChecksum)
}

func burnMemoryWorker(ctx context.Context, size uint64, bytes, checksum *atomic.Uint64, group *sync.WaitGroup) {
	defer group.Done()
	if size > uint64(^uint(0)>>1) {
		return
	}
	half := int(size / 2)
	source, destination := make([]byte, half), make([]byte, half)
	for index := range source {
		source[index] = byte(index)
	}
	count, localChecksum := uint64(0), uint64(0)
	for ctx.Err() == nil {
		copy(destination, source)
		localChecksum += uint64(destination[count%uint64(len(destination))])
		count++
	}
	bytes.Add(count * uint64(half))
	checksum.Add(localChecksum)
}
