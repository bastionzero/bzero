package bzio

import (
	"io"
	"os"
)

// an interface providing methods to interact with readers and writers
// for now, restricted to Stdin/Stdout
type BzIo interface {
	io.ReadWriter
	WriteErr(b []byte) (n int, err error)
}

// the default implementation
type StdIo struct{}

func (s StdIo) Read(b []byte) (n int, err error) {
	return os.Stdin.Read(b)
}

func (s StdIo) Write(b []byte) (n int, err error) {
	return os.Stdout.Write(b)
}

func (s StdIo) WriteErr(b []byte) (n int, err error) {
	return os.Stderr.Write(b)
}
