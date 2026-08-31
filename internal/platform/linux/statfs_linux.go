//go:build linux

package linux

import (
	"syscall"

	"toolctl/internal/core"
)

func (FileSystem) StatFS(path string) (core.FileSystemStats, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return core.FileSystemStats{}, err
	}
	blockSize := uint64(stat.Bsize)
	return core.FileSystemStats{
		TotalBytes:     stat.Blocks * blockSize,
		FreeBytes:      stat.Bfree * blockSize,
		AvailableBytes: stat.Bavail * blockSize,
	}, nil
}
