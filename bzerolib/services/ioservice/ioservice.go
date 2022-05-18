package ioservice

import (
	"bufio"
	"io"
	"os"
)

// TODO: docstring
type IoService interface {
	io.ReadWriter
	NewScanner(r io.Reader) *bufio.Scanner
}

// TODO: docstring
type StdIoService struct{}

func (s StdIoService) Read(b []byte) (n int, err error) {
	return os.Stdin.Read(b)
}

func (s StdIoService) Write(b []byte) (n int, err error) {
	return os.Stdout.Write(b)
}

func (s StdIoService) NewScanner(r io.Reader) *bufio.Scanner {
	return bufio.NewScanner(r)
}
