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
	"bastionzero.com/bctl/v1/bzerolib/unix/unixuser"
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
	// Create will create the user with the given username if it is allowed, or it will return the existing user
	if usr, err := unixuser.LookupOrCreateFromList(runAsUser); err != nil {
		return nil, fmt.Errorf("failed to use ssh as user %s: %s", runAsUser, err)
	} else {
		return pseudoterminal.New(logger, usr, command)
	}
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
	logger *logger.Logger

	runAsUser            string
	doneChan             chan struct{}
	streamOutputChan     chan smsg.StreamMessage
	streamSequenceNumber int
	streamMessageVersion smsg.SchemaVersion

	// stdout circular buffer
	ringBuffer *ringbuffer.RingBuffer

	// interface for interacting with pty
	terminal IPseudoTerminal
}

// New returns a new instance of the DefaultShell
func New(
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	doneChan chan struct{},
	runAsUser string) *DefaultShell {
	return &DefaultShell{
		logger:               logger,
		runAsUser:            runAsUser,
		doneChan:             doneChan,
		streamOutputChan:     ch,
		streamSequenceNumber: 1,
	}
}

func (d *DefaultShell) Kill() {
	if d.terminal != nil {
		d.terminal.Kill()
		d.terminal = nil

		// Wait for done channel to be closed by writePump
		<-d.doneChan
	}
}

// Receive takes input from a client using the MRZAP datachannel and returns output via the MRZAP datachannel
func (d *DefaultShell) Receive(action string, actionPayload []byte) ([]byte, error) {
	d.logger.Infof("Plugin received Data message with %v action", action)

	switch bzshell.ShellSubAction(action) {
	case bzshell.ShellOpen:
		// We ignore the RunAsUser in the Shell/Open message since it was set by
		// the plugin when it processes the SYN message. This is important
		// because the RunAsUser is part of policy and the policy check happens
		// based on the SYN message when the datachannel is first opened.

		var shellOpen bzshell.ShellOpenMessage
		if err := json.Unmarshal(actionPayload, &shellOpen); err != nil {
			rerr := fmt.Errorf("malformed shell open payload: %s %+v", err, actionPayload)
			d.logger.Error(rerr)
			return []byte{}, rerr
		}
		d.streamMessageVersion = shellOpen.StreamMessageVersion

		if err := d.open(); err != nil {
			d.logger.Error(err)
			return []byte{}, err
		}
	case bzshell.ShellClose:
		d.Kill()
	case bzshell.ShellInput:
		var shellInput bzshell.ShellInputMessage
		if err := json.Unmarshal(actionPayload, &shellInput); err != nil {
			rerr := fmt.Errorf("malformed shell input payload: %s %+v", err, actionPayload)
			d.logger.Error(rerr)
			return []byte{}, rerr
		}

		if err := d.writeToTerminal(shellInput.Data); err != nil {
			d.logger.Error(err)
			return []byte{}, err
		}
	case bzshell.ShellResize:
		var shellResize bzshell.ShellResizeMessage
		if err := json.Unmarshal(actionPayload, &shellResize); err != nil {
			rerr := fmt.Errorf("malformed shell resize payload: %s %+v", err, actionPayload)
			d.logger.Error(rerr)
			return []byte{}, rerr
		}

		if err := d.setSize(shellResize.Cols, shellResize.Rows); err != nil {
			d.logger.Error(err)
			return []byte{}, err
		}
	case bzshell.ShellReplay:
		if replayBytes, err := d.ringBuffer.ReadAll(); err != nil {
			return []byte{}, fmt.Errorf("failed to read from stdout buff for shell replay %s", err)
		} else {
			return replayBytes, nil
		}

	default:
		return []byte{}, fmt.Errorf("unrecognized shell action received: %s", action)
	}

	return []byte{}, nil
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
	defer d.Kill()
	defer close(d.doneChan)
	defer func() {
		if err := recover(); err != nil {
			d.logger.Errorf("WritePump thread crashed with message: %s", err)
		}
	}()

	d.ringBuffer = ringbuffer.New(shellStdOutBuffCapacity)
	stdoutBuff := make([]byte, streamDataPayloadSize)
	stdOut := d.terminal.StdOut()

	for {
		select {
		case <-d.terminal.Done():
			d.logger.Infof("pty command exited sending stop stream message")
			d.sendStreamMessage(smsg.Stop, []byte{})
			return
		default:
			if stdoutBytesLen, err := stdOut.Read(stdoutBuff); err != nil {
				d.sendStreamMessage(smsg.Stop, stdoutBuff[:stdoutBytesLen])
				d.logger.Errorf("error reading from stdout: %s", err)
				return
			} else {
				d.ringBuffer.Write(stdoutBuff[:stdoutBytesLen])
				d.sendStreamMessage(smsg.StdOut, stdoutBuff[:stdoutBytesLen])
			}

			// Wait for stdout to process more data
			time.Sleep(time.Millisecond)
		}
	}
}

func (d *DefaultShell) sendStreamMessage(streamType smsg.StreamType, content []byte) {
	message := smsg.StreamMessage{
		SchemaVersion:  d.streamMessageVersion,
		Action:         "shell/default",
		Type:           streamType,
		SequenceNumber: d.streamSequenceNumber,
		Content:        base64.StdEncoding.EncodeToString(content),
	}

	d.streamOutputChan <- message
	d.streamSequenceNumber++
}
