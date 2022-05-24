package fileservice

import (
	"io/fs"

	"github.com/stretchr/testify/mock"
)

// mocked version of the FileService
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

func (m MockFileService) Remove(name string) error {
	args := m.Called(name)
	return args.Error(0)
}
