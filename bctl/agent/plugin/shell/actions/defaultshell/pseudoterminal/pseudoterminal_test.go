package pseudoterminal

import (
	"fmt"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/unix/unixuser"
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

func getPseudoTerminal() (*PseudoTerminal, error) {
	commandstr := ""
	logger := logger.MockLogger()
	if usr, err := unixuser.Current(); err != nil {
		return nil, err
	} else if terminal, err := New(logger, usr, commandstr); err != nil {
		return nil, fmt.Errorf("failed to create new pseudo terminal: %s", err)
	} else {
		return terminal, nil
	}

}
