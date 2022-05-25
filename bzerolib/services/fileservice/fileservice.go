package fileservice

import (
	"io/fs"
	"os"
)

// an interface providing ways to interact with files
// can be implemented by native os methods or dummy functions for testing
type FileService interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm fs.FileMode) error
}

// the default implementation
type OsFileService struct{}

func (f OsFileService) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (f OsFileService) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}
