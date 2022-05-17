package ioservice

import (
	"github.com/stretchr/testify/mock"
)

// TODO: docstring
type MockIoService struct {
	IoService
	mock.Mock
}

func (m MockIoService) Read(b []byte) (n int, err error) {
	args := m.Called()
	return args.Int(0), args.Error(1)
}

func (m MockIoService) Write(b []byte) (n int, err error) {
	args := m.Called(b)
	return args.Int(0), args.Error(1)
}
