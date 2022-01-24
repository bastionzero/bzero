package mockpty

import (
	"bytes"
	"fmt"
	"os/exec"
	"syscall"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/shell/termpty"
	"github.com/creack/pty"
)

func Start(cmd *exec.Cmd) (termpty.IFile, error) {
	return StartWithSize(cmd, nil)
}

func StartWithSize(cmd *exec.Cmd, ws *pty.Winsize) (termpty.IFile, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	return StartWithAttrs(cmd, ws, cmd.SysProcAttr)
}

func StartWithAttrs(c *exec.Cmd, sz *pty.Winsize, attrs *syscall.SysProcAttr) (termpty.IFile, error) {
	pty, _, err := Open()

	if err != nil {
		return nil, err
	}
	return pty, nil
}

var mockptyExpIn = []string{"testdata-1", "testdata-2"}
var mockptyExpOut = []string{"testdata-3", "testdata-4"}

func Open() (pty, tty termpty.IFile, err error) {

	var mfile termpty.IFile
	mfile, _ = MockPtyFileNew(mockptyExpIn, mockptyExpOut)
	return mfile, nil, nil
}

type MockPtyFile struct {
	expectedInput  []string
	expectedOutput []string
	inputPos       int
	outputPos      int
}

func MockPtyFileNew(expin, expout []string) (*MockPtyFile, error) {
	var mockfile = MockPtyFile{
		expectedInput:  expin,
		expectedOutput: expout,
		inputPos:       0,
		outputPos:      0,
	}
	return &mockfile, nil
}

func (m *MockPtyFile) Read(b []byte) (n int, err error) {
	bytesToRead := []byte(m.expectedOutput[m.outputPos])
	m.outputPos += 1

	if len(b) < len(bytesToRead) {
		fmt.Errorf("Buffer b not large enough, got buffer of size %d but required buffer of size %d", len(b), len(bytesToRead))
		return 0, nil
	}

	n = copy(b, bytesToRead)

	return n, nil
}

func (m *MockPtyFile) Write(b []byte) (n int, err error) {
	expectedBytes := []byte(m.expectedInput[m.inputPos])
	m.inputPos += 1

	if bytes.Compare(expectedBytes, b) != 0 {
		err := fmt.Errorf("Mock pty expected %v but got %v", string(expectedBytes), string(b))
		return 0, err
	}
	return len(b), nil
}

func (m *MockPtyFile) Close() error {
	return nil
}

func (m *MockPtyFile) Moo() {
}

// func (m *MockPtyFile) ReadAt(b []byte, off int64) (n int, err error) {
// 	return 0, nil
// }
// func (m *MockPtyFile) WriteAt(b []byte, off int64) (n int, err error) {
// 	return 0, nil
// }
