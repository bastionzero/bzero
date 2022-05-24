package ioservice

import (
	"io"
	"os"
)

// an interface providing methods to interact with readers and writers
// for now, restricted to Stdin/Stdout and a scanner
type IoService interface {
	io.ReadWriter
}

// the default implementation
type StdIoService struct{}

func (s StdIoService) Read(b []byte) (n int, err error) {
	return os.Stdin.Read(b)
}

func (s StdIoService) Write(b []byte) (n int, err error) {
	return os.Stdout.Write(b)
}
