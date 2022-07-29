package bzio

import (
	"io/fs"

	"github.com/stretchr/testify/mock"
)

// mocked version of BzIo
type MockBzIo struct {
	mock.Mock
	TestData string
}

func (m MockBzIo) Read(b []byte) (n int, err error) {
	args := m.Called()
	copy(b, []byte(m.TestData))
	return args.Int(0), args.Error(1)
}

func (m MockBzIo) Write(b []byte) (n int, err error) {
	args := m.Called(b)
	return args.Int(0), args.Error(1)
}

func (m MockBzIo) WriteErr(b []byte) (n int, err error) {
	args := m.Called(b)
	return args.Int(0), args.Error(1)
}

// mocked version of BzFileIo
type MockBzFileIo struct {
	mock.Mock
}

func (m MockBzFileIo) ReadFile(name string) ([]byte, error) {
	args := m.Called(name)
	return args.Get(0).([]byte), args.Error(1)
}

func (m MockBzFileIo) WriteFile(name string, data []byte, perm fs.FileMode) error {
	args := m.Called(name)
	return args.Error(0)
}

func (m MockBzFileIo) Remove(name string) error {
	args := m.Called(name)
	return args.Error(0)
}
