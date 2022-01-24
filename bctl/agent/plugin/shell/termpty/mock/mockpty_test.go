package mockpty

import (
	"bufio"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateMockPty(t *testing.T) {
	cmd := exec.Command("sh")

	ptyf, err := Start(cmd)
	if err != nil {
		t.Errorf("Error thrown will creating mock pty file: %v", err)
	}

	assert.NotNil(t, ptyf)
}

func TestReadWriteMockPty(t *testing.T) {
	mockptyExpIn = []string{"ls -l\n", "exit"}
	mockptyExpOut = []string{"255 sh$ ", "total"}

	cmd := exec.Command("sh")

	ptyf, err := Start(cmd)
	if err != nil {
		t.Errorf("Error thrown will creating mock pty file: %v", err)
	}

	assert.NotNil(t, ptyf)

	readBytes := make([]byte, 1024)
	reader := bufio.NewReader(ptyf)

	readBytesLen, err := reader.Read(readBytes)
	if err != nil {
		t.Errorf("Read from mock pty failed when reading from stdout: %v", err)
	}

	readStr := string(readBytes[:readBytesLen])
	assert.EqualValues(t, "255 sh$ ", readStr)

	inputbytes := []byte("ls -l\n")
	readBytesLen, err = ptyf.Write(inputbytes)
	if err != nil {
		t.Errorf("Write to mock pty failed when writign to stdin: %v", err)
	}

	readBytesLen, err = reader.Read(readBytes)
	if err != nil {
		t.Errorf("Read from mock pty failed when reading from stdout: %v", err)
	}

	readStr = string(readBytes[:readBytesLen])
	assert.EqualValues(t, "total", readStr)

}
