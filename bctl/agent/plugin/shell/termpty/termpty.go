package termpty

import (
	"io"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

type IPty interface {
	Start(cmd *exec.Cmd) (IFile, error)
	StartWithSize(cmd *exec.Cmd, ws *pty.Winsize) (IFile, error)
	StartWithAttrs(c *exec.Cmd, sz *pty.Winsize, attrs *syscall.SysProcAttr) (IFile, error)
	Open() (pty, tty IFile, err error)
	Setsize(ptyFile IFile, wq *pty.Winsize)
}

type IFile interface {
	io.Closer
	io.Reader
	io.Writer
	// Write(p []byte) (n int, err error)
	// io.ReaderAt
	// io.Seeker
	// Stat() (os.FileInfo, error)
}

// We wrap the pty commands to force them to use IFile rather than os.File. This lets us swap out pty or for our mock pty

func Start(cmd *exec.Cmd) (IFile, error) {
	return pty.Start(cmd)
}

func StartWithSize(cmd *exec.Cmd, ws *pty.Winsize) (IFile, error) {
	return pty.StartWithSize(cmd, ws)
}

func StartWithAttrs(c *exec.Cmd, sz *pty.Winsize, attrs *syscall.SysProcAttr) (IFile, error) {
	return pty.StartWithAttrs(c, sz, attrs)
}

func Open() (IFile, IFile, error) {
	return pty.Open()
}

// func Setsize(ptyFile IFile, wq *pty.Winsize) {
// 	//nolint:gosec // Expected unsafe pointer for Syscall call.
// 	ioctl(ptyFile.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(ws)))
// }
