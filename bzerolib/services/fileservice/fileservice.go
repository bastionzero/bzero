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
	Open(name string) (*os.File, error)
	MkdirAll(path string, perm os.FileMode) error
	Append(path string, contents string) error
}

// the default implementation
type OsFileService struct{}

func (f OsFileService) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (f OsFileService) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}

func (f OsFileService) Open(name string) (*os.File, error) {
	return os.Open(name)
}

func (f OsFileService) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (f OsFileService) Append(path string, contents string) error {
	// If the file doesn't exist, create it, or append to the file
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	defer func() {
		file.Close()
	}()

	if err != nil {
		return err
	}

	if _, err := file.Write([]byte(contents)); err != nil {
		return err
	}

	return nil
}
