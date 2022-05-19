package fileservice

import (
	"io/fs"
	"os"

	"github.com/stretchr/testify/mock"
)

// TODO: docstring
type MockFileService struct {
	FileService
	mock.Mock
}

func (m MockFileService) ReadFile(name string) ([]byte, error) {
	args := m.Called(name)
	return args.Get(0).([]byte), args.Error(1)
}

func (m MockFileService) WriteFile(name string, data []byte, perm fs.FileMode) error {
	args := m.Called(name)
	return args.Error(0)
}

func (m MockFileService) Open(name string) (*os.File, error) {
	args := m.Called(name)
	return args.Get(0).(*os.File), args.Error(1)
}

func (m MockFileService) MkdirAll(path string, perm os.FileMode) error {
	args := m.Called(path, perm)
	return args.Error(0)
}

func (m MockFileService) Append(path string, contents string) error {
	args := m.Called(path, contents)
	return args.Error(0)
}
