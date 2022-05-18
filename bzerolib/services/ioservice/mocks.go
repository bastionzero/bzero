package ioservice

import (
	"bufio"
	"io"

	"github.com/stretchr/testify/mock"
)

// TODO: docstring
type MockIoService struct {
	IoService
	mock.Mock
	TestData string
}

func (m MockIoService) Read(b []byte) (n int, err error) {
	args := m.Called()
	copy(b, []byte(m.TestData))
	return args.Int(0), args.Error(1)
}

func (m MockIoService) Write(b []byte) (n int, err error) {
	args := m.Called(b)
	return args.Int(0), args.Error(1)
}

func (m MockIoService) NewScanner(r io.Reader) *bufio.Scanner {
	args := m.Called()
	return args.Get(0).(*bufio.Scanner)
}
