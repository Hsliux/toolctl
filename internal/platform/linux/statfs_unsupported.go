//go:build !linux

package linux

import (
	"fmt"

	"toolctl/internal/core"
)

func (FileSystem) StatFS(string) (core.FileSystemStats, error) {
	return core.FileSystemStats{}, fmt.Errorf("statfs is only supported on linux")
}
