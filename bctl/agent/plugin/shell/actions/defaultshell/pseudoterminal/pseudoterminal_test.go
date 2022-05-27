package pseudoterminal

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"github.com/stretchr/testify/assert"
)

func TestPseudoTerminalCreation(t *testing.T) {
	if terminal, err := getPseudoTerminal(); err != nil {
		t.Errorf("failed to create new pseudo terminal: %s", err)
	} else {
		assert.NotNil(t, terminal)
		assert.NotNil(t, terminal.command)
		assert.NotNil(t, terminal.ptyFile)
		assert.NotNil(t, terminal.logger)

		assert.NotNil(t, terminal.StdIn())
		assert.NotNil(t, terminal.StdOut())
	}
}

func TestRunCommand(t *testing.T) {
	// we use a command that requires calculation so that we don't confuse an error that
	// outputs the entire string with a successful execution
	keystrokes := "declare -i myvar=5+1; echo $myvar\n"
	expectedOutput := "6"

	if terminal, err := getPseudoTerminal(); err != nil {
		t.Errorf("failed to create new pseudo terminal: %s", err)
	} else {
		for _, char := range keystrokes {
			if _, err := terminal.StdIn().Write([]byte(string(char))); err != nil {
				t.Errorf("Unable to write to stdin: %s", err)
			}
		}
		time.Sleep(1 * time.Second) // let the command run

		stdoutBytes := make([]byte, 1000)
		if n, err := terminal.StdOut().Read(stdoutBytes); err != nil {
			t.Errorf("failed to read from stdout: %s", err)
		} else {
			assert.Contains(t, string(stdoutBytes[:n]), expectedOutput)
		}
	}
}

func TestShutdown(t *testing.T) {
	if terminal, err := getPseudoTerminal(); err != nil {
		t.Errorf("failed to create new pseudo terminal: %s", err)
	} else {
		for {
			go func() {
				time.Sleep(1 * time.Second)
				terminal.Kill()
			}()

			select {
			case <-terminal.Done():
				return
			case <-time.After(5 * time.Second):
				t.Error("terminal failed to die")
			}
		}
	}
}

func TestSetSize(t *testing.T) {
	if terminal, err := getPseudoTerminal(); err != nil {
		t.Error(err)
	} else {
		assert.Nil(t, terminal.SetSize(10, 10))
	}
}

func TestDoesUserExist(t *testing.T) {
	shellCommand := defaultShellCommand
	shellCommandArgs := []string{"-c"}

	realUser, err := whoAmI()
	if err != nil {
		t.Error("failed to grab current user")
	}
	assert.Nil(t, doesUserExist(realUser, shellCommand, shellCommandArgs))

	fakeUser := "MonsieurFake"
	assert.NotNil(t, doesUserExist(fakeUser, shellCommand, shellCommandArgs))
}

func getPseudoTerminal() (*PseudoTerminal, error) {
	logger := logger.DevNullLogger()
	runAsUser, err := whoAmI()
	if err != nil {
		return nil, fmt.Errorf("failed to grab current user")
	}
	commandstr := ""

	if terminal, err := New(logger, runAsUser, commandstr); err != nil {
		return nil, fmt.Errorf("failed to create new pseudo terminal: %s", err)
	} else {
		return terminal, nil
	}
}

// whoAmI returns the current username that the agent is running under
func whoAmI() (string, error) {
	cmdstr := "whoami"
	shellCmdArgs := append([]string{"-c"}, cmdstr)
	cmd := exec.Command(defaultShellCommand, shellCmdArgs...)
	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// The program has exited with an exit code != 0
			return "", fmt.Errorf("encountered an error while running command %v : %s", cmdstr, exitErr)
		}
		return "", nil
	}

	return strings.TrimSpace(string(stdout)), nil
}
