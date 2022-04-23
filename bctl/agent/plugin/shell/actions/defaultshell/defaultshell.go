package defaultshell

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/shell/actions/defaultshell/pseudoterminal"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	"bastionzero.com/bctl/v1/bzerolib/ringbuffer"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

// DefaultShell - Allows launching an interactive shell on the host which the agent is running on. Implements IShellAction.
//
//     New - Configures the shell including setting the RunAsUser but doesn't launch it
//     Receive - receives MRZAP messages and dispatches them to the correct method based on subaction
//
// User interaction works as follows:
//     user keypresses --> DataChannel --> plugin.Receive(shell/input) --> p.action.Receive(shell/input) -> p.ShellInput(..) --> pty.stdIn
//     user terminal <-- streamOutputChan <-- p.writePump(...) <-- pty.stdOut <-- terminal output
//
// We wrap our pty in a structure called the PseudoTerminal which allows us to create and interact with a shell
//     StdIn - returns which file to write to
//     StdOut - returns which file to read from
//     SetSize - changes the terminal size window
//     Done - a channel for letting the action know when the terminal has ended
//     Kill - kills the command

// for testing purposes this needs to be a variable so that we can overwrite it with our mocked version in test
var NewPseudoTerminal = func(logger *logger.Logger, runAsUser string, command string) (IPseudoTerminal, error) {
	return pseudoterminal.New(logger, runAsUser, command)
}

const (
	// Buffer capacity of 100000 items with each buffer item of 1024 bytes leads to max usage of 100MB (100000 * 1024 bytes = 100MB) of instance memory.
	// When changing streamDataPayloadSize, make corresponding change to buffer capacity to ensure no more than 100MB of instance memory is used.
	streamDataPayloadSize   = 1024
	shellStdOutBuffCapacity = 10 * 1000
)

type IPseudoTerminal interface {
	StdIn() io.Writer
	StdOut() io.Reader
	SetSize(cols, rows uint32) error
	Done() <-chan struct{}
	Kill()
}

type DefaultShell struct {
	tmb    *tomb.Tomb // datachannel's tomb
	logger *logger.Logger

	runAsUser string

	streamOutputChan     chan smsg.StreamMessage
	streamSequenceNumber int

	// stdout circular buffer
	stdoutbuff *ringbuffer.RingBuffer

	// interface for interacting with pty
	terminal IPseudoTerminal
}

// New returns a new instance of the DefaultShell
func New(
	parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	runAsUser string) (*DefaultShell, error) {
	return &DefaultShell{
		runAsUser:            runAsUser,
		logger:               logger,
		tmb:                  parentTmb, // if datachannel dies, so should we
		streamOutputChan:     ch,
		streamSequenceNumber: 1,
	}, nil
}

// Receive takes input from a client using the MRZAP datachannel and returns output via the MRZAP datachannel
func (d *DefaultShell) Receive(action string, actionPayload []byte) (string, []byte, error) {
	d.logger.Infof("Plugin received Data message with %v action", action)

	switch bzshell.ShellSubAction(action) {
	case bzshell.ShellOpen:
		// We ignore the RunAsUser in the Shell/Open message since it was set by
		// the plugin when it processes the SYN message. This is important
		// because the RunAsUser is part of policy and the policy check happens
		// based on the SYN message when the datachannel is first opened.
		if err := d.open(); err != nil {
			d.logger.Error(err)
			return "", []byte{}, err
		}
	case bzshell.ShellClose:
		d.terminal.Kill()
	case bzshell.ShellInput:
		var shellInput bzshell.ShellInputMessage
		if err := json.Unmarshal(actionPayload, &shellInput); err != nil {
			rerr := fmt.Errorf("malformed shell input payload: %s %+v", err, actionPayload)
			d.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		if err := d.writeToTerminal(shellInput.Data); err != nil {
			d.logger.Error(err)
			return action, []byte{}, err
		}
	case bzshell.ShellResize:
		var shellResize bzshell.ShellResizeMessage
		if err := json.Unmarshal(actionPayload, &shellResize); err != nil {
			rerr := fmt.Errorf("malformed shell resize payload: %s %+v", err, actionPayload)
			d.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		if err := d.setSize(shellResize.Cols, shellResize.Rows); err != nil {
			d.logger.Error(err)
			return action, []byte{}, err
		}
	case bzshell.ShellReplay:
		if replayBytes, err := d.stdoutbuff.ReadAll(); err != nil {
			return action, []byte{}, fmt.Errorf("failed to read from stdout buff for shell replay %s", err)
		} else {
			return action, replayBytes, nil
		}

	default:
		return action, []byte{}, fmt.Errorf("unrecognized shell action received: %s", action)
	}

	return action, []byte{}, nil
}

func (d *DefaultShell) open() error {
	// If this method "open" is called twice is means something has gone very
	// wrong and failing early is the safest action.
	if d.terminal != nil {
		return fmt.Errorf("attempted to start the shell but a call to open a shell has already been made")
	}

	if terminal, err := NewPseudoTerminal(d.logger, d.runAsUser, ""); err != nil {
		return err
	} else {
		d.terminal = terminal
	}

	go func() {
		select {
		case <-d.tmb.Dying():
			d.terminal.Kill()
			return
		case <-d.terminal.Done():
			return
		}
	}()

	go d.writePump()

	return nil
}

// writeToTerminal passes payload byte stream to shell stdin
func (d *DefaultShell) writeToTerminal(keystrokes []byte) error {
	if d.terminal == nil {
		return fmt.Errorf("could not process input; no terminal associated with this action")
	} else if _, err := d.terminal.StdIn().Write(keystrokes); err != nil {
		d.logger.Errorf("Unable to write to stdin: %s", err)
		return err
	} else {
		return nil
	}
}

// setSize resizes the pseudo-terminal pty
func (d *DefaultShell) setSize(cols, rows uint32) error {
	d.logger.Debugf("default shell received resize: {cols: %d, rows: %d}", cols, rows)

	if d.terminal == nil {
		return fmt.Errorf("can't set size of non-existant terminal")
	} else if err := d.terminal.SetSize(cols, rows); err != nil {
		return err
	} else {
		return nil
	}
}

// writePump reads from pty stdout and writes to datachannel.
func (d *DefaultShell) writePump() {
	defer func() {
		if err := recover(); err != nil {
			d.logger.Errorf("WritePump thread crashed with message: %s", err)
		}
	}()

	d.stdoutbuff = ringbuffer.New(shellStdOutBuffCapacity)
	stdoutBytes := make([]byte, streamDataPayloadSize)

	// Wait for all input commands to run.
	time.Sleep(time.Second)

	for {
		select {
		case <-d.terminal.Done():
			d.logger.Infof("pty command exited sending stop stream message")
			d.sendStreamMessage(smsg.Stop, []byte{})
			return
		default:
			if stdoutBytesLen, err := d.terminal.StdOut().Read(stdoutBytes); err != nil {
				d.sendStreamMessage(smsg.Stop, stdoutBytes[:stdoutBytesLen])
				d.logger.Errorf("error reading from stdout: %s", err)
				return
			} else {
				d.stdoutbuff.Write(stdoutBytes[:stdoutBytesLen])
				d.sendStreamMessage(smsg.StdOut, stdoutBytes[:stdoutBytesLen])
			}

			// Wait for stdout to process more data
			time.Sleep(time.Millisecond)
		}
	}
}

func (d *DefaultShell) sendStreamMessage(streamType smsg.StreamType, content []byte) {
	message := smsg.StreamMessage{
		Action:         "shell/default",
		Type:           streamType,
		SequenceNumber: d.streamSequenceNumber,
		Content:        base64.StdEncoding.EncodeToString(content),
	}

	d.streamOutputChan <- message
	d.streamSequenceNumber++
}
