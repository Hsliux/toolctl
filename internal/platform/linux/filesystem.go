package linux

import (
	"fmt"
	"io"
	"io/fs"
	"os"
)

type FileSystem struct{}

func NewFileSystem() FileSystem { return FileSystem{} }

func (FileSystem) ReadFile(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := io.LimitReader(file, maxBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file %s exceeds %d bytes", path, maxBytes)
	}
	return data, nil
}

func (FileSystem) ReadDir(path string) ([]fs.DirEntry, error) { return os.ReadDir(path) }

func (FileSystem) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }
