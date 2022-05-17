package fileservice

import (
	"io/fs"
	"os"
)

// an interface providing ReadFile and WriterFile methods
// can be implemented by native os methods or dummy functions for testing
type FileService interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm fs.FileMode) error
}

// TODO: docstring
type OsFileService struct{}

func (OsFileService) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (OsFileService) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}
