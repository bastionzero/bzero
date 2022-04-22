package defaultshell

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	InputBufferSize   = 8 * 1024
	InputDebounceTime = 5 * time.Millisecond
)

type DefaultShell struct {
	logger *logger.Logger
	tmb    *tomb.Tomb

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage

	// channel where we push each individual keypress byte from StdIn
	stdInChan chan byte
}

func New(logger *logger.Logger) (*DefaultShell, chan plugin.ActionWrapper) {

	shellAction := &DefaultShell{
		logger: logger,

		outputChan:      make(chan plugin.ActionWrapper, 10),
		streamInputChan: make(chan smsg.StreamMessage, 30),
		stdInChan:       make(chan byte, InputBufferSize),
	}

	return shellAction, shellAction.outputChan
}

func (d *DefaultShell) Start(tmb *tomb.Tomb, attach bool) error {
	d.tmb = tmb

	if attach {
		// If we are attaching send a shell replay message to replay terminal
		// output
		shellReplayDataMessage := bzshell.ShellReplayMessage{}
		d.sendOutputMessage(bzshell.ShellReplay, shellReplayDataMessage)
	} else {
		// If we are not attaching then send a ShellOpen data message to start
		// the pty on the target
		openShellDataMessage := bzshell.ShellOpenMessage{
			// note the TargetUser in this data message is ignored by the agent
			// because it is policy-checked by bzero when its sent in the SYN
			// message when opening the datachannel and should never be changed
			// afterwards
			TargetUser: "",
		}
		d.sendOutputMessage(bzshell.ShellOpen, openShellDataMessage)
	}

	// Set initial terminal dimensions and then listen for any changes to
	// terminal size
	d.sendTerminalSize()
	d.listenForTerminalSizeChanges()

	// listen to stream messages on input chan and write to stdout
	go d.handleStreamMessages()

	// reading Stdin in raw mode and forward keypresses after debouncing
	go d.readStdIn()
	go d.sendStdIn()

	return nil
}

func (d *DefaultShell) Replay(replayData []byte) error {
	d.logger.Debug("Default shell received replay message with action")
	if _, err := os.Stdout.Write(replayData); err != nil {
		d.logger.Errorf("Error writing shell replay message to Stdout: %s", err)
		return err
	}

	return nil
}

func (d *DefaultShell) ReceiveStream(smessage smsg.StreamMessage) {
	d.logger.Debugf("Default shell received %v stream, message count: %d", smessage.Type, len(d.streamInputChan)+1)
	d.streamInputChan <- smessage
}

func (d *DefaultShell) handleStreamMessages() {
	for {
		select {
		case <-d.tmb.Dying():
			return
		case streamMessage := <-d.streamInputChan:
			// process the incoming stream messages
			switch smsg.StreamType(streamMessage.Type) {
			case smsg.StdOut:
				if contentBytes, err := base64.StdEncoding.DecodeString(streamMessage.Content); err != nil {
					d.logger.Errorf("Error decoding ShellStdOut stream content: %s", err)
				} else {
					if _, err = os.Stdout.Write(contentBytes); err != nil {
						d.logger.Errorf("Error writing to Stdout: %s", err)
					}
				}
			case smsg.Stop:
				d.tmb.Kill(fmt.Errorf("received shell quit stream message"))
				return
			default:
				d.logger.Errorf("unhandled stream type: %s", streamMessage.Type)
			}
		}
	}
}

// Reads from StdIn and pushes to an input channel
func (d *DefaultShell) readStdIn() {
	// switch stdin into 'raw' mode
	// https://pkg.go.dev/golang.org/x/term#pkg-overview
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		d.logger.Errorf("Error switching std to raw mode: %s", err)
		return
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	b := make([]byte, 1)

	for {
		select {
		case <-d.tmb.Dying():
			return
		default:
			n, err := os.Stdin.Read(b)
			if err != nil || n != 1 {
				d.tmb.Kill(fmt.Errorf("error reading last keypress from Stdin: %s", err))
				return
			}
			d.logger.Infof("READ")

			d.stdInChan <- b[0]
		}
	}
}

// processes input channel by debouncing all keypresses within a time interval
func (d *DefaultShell) sendStdIn() {
	inputBuf := make([]byte, InputBufferSize)

	for {
		select {
		case <-d.tmb.Dying():
			return
		case b := <-d.stdInChan:
			inputBuf = append(inputBuf, b)
		case <-time.After(InputDebounceTime):
			if len(inputBuf) >= 1 {
				// Send all accumulated keypresses in a shellInput data message
				shellInputDataMessage := bzshell.ShellInputMessage{
					Data: inputBuf,
				}
				d.sendOutputMessage(bzshell.ShellInput, shellInputDataMessage)

				// clear the input buffer by slicing it to size 0 which will still
				// keep memory allocated for the underlying capacity of the slice
				inputBuf = inputBuf[:0]
			}
		}
	}
}

func (d *DefaultShell) sendTerminalSize() {
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err != nil {
		d.logger.Errorf("Failed to get current terminal size %s", err)
	} else {
		shellResizeMessage := bzshell.ShellResizeMessage{
			Rows: uint32(h),
			Cols: uint32(w),
		}
		d.sendOutputMessage(bzshell.ShellResize, shellResizeMessage)
	}
}

func (d *DefaultShell) sendOutputMessage(action bzshell.ShellSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	d.outputChan <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}
}

// Captures any terminal resize events using the SIGWINCH signal and send the
// new terminal size
func (d *DefaultShell) listenForTerminalSizeChanges() {
	ch := make(chan os.Signal, 1)
	sig := unix.SIGWINCH

	signal.Notify(ch, sig)
	go func() {
		for {
			select {
			case <-d.tmb.Dying():
				signal.Reset(sig)
				close(ch)
				return
			case <-ch:
				d.sendTerminalSize()
			}
		}
	}()
}
