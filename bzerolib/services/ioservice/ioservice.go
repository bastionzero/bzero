package ioservice

import (
	"io"
	"os"
)

// TODO: docstring
type IoService interface {
	io.ReadWriter
}

// TODO: docstring
type StdIoService struct{}

func (s StdIoService) Read(b []byte) (n int, err error) {
	return os.Stdin.Read(b)
}

func (s StdIoService) Write(b []byte) (n int, err error) {
	return os.Stdout.Write(b)
}
