package unixshell

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

type UnixShell struct {
	logger *logger.Logger
	tmb    *tomb.Tomb

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage

	// channel where we push each individual keypress byte from StdIn
	stdInChan chan byte
}

func New(logger *logger.Logger) (*UnixShell, chan plugin.ActionWrapper) {

	shellAction := &UnixShell{
		logger: logger,

		outputChan:      make(chan plugin.ActionWrapper, 10),
		streamInputChan: make(chan smsg.StreamMessage, 30),
		stdInChan:       make(chan byte, InputBufferSize),
	}

	return shellAction, shellAction.outputChan
}

func (u *UnixShell) Start(tmb *tomb.Tomb, attach bool) error {
	u.tmb = tmb

	if attach {
		// If we are attaching send a shell replay message to replay terminal
		// output
		shellReplayDataMessage := bzshell.ShellReplayMessage{}
		u.sendOutputMessage(bzshell.ShellReplay, shellReplayDataMessage)
	} else {
		// If we are not attaching then send a ShellOpen data message to start
		// the pty on the target
		openShellDataMessage := bzshell.ShellOpenMessage{
			// note the TargetUser in this data message is ignored by the agent
			// because it is policy-checked by bzero when its sent in the SYN
			// message when opening the data channel and should never be changed
			// afterwards
			TargetUser: "",
		}
		u.sendOutputMessage(bzshell.ShellOpen, openShellDataMessage)
	}

	// Set initial terminal dimensions and then listen for any changes to
	// terminal size
	u.sendTerminalSize()
	u.listenForTerminalSizeChanges()

	// listen to stream messages on input chan and write to stdout
	go u.handleStreamMessages()

	// reading Stdin in raw mode and forward keypresses after debouncing
	go u.readStdIn()
	go u.processStdIn()

	return nil
}

func (u *UnixShell) Replay(replayData []byte) error {
	u.logger.Debug("Unix shell received replay message with action")
	if _, err := os.Stdout.Write(replayData); err != nil {
		u.logger.Errorf("Error writing shell replay message to Stdout: %s", err)
		return err
	}

	return nil
}

func (u *UnixShell) ReceiveStream(smessage smsg.StreamMessage) {
	u.logger.Debugf("Unix shell received %v stream, message count: %d", smessage.Type, len(u.streamInputChan)+1)
	u.streamInputChan <- smessage
}

func (u *UnixShell) sendOutputMessage(action bzshell.ShellSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	u.outputChan <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}
}

func (u *UnixShell) handleStreamMessages() {
	for {
		select {
		case <-u.tmb.Dying():
			return
		case streamMessage := <-u.streamInputChan:
			// process the incoming stream messages
			switch smsg.StreamType(streamMessage.Type) {
			case smsg.StdOut:
				if contentBytes, err := base64.StdEncoding.DecodeString(streamMessage.Content); err != nil {
					u.logger.Errorf("Error decoding ShellStdOut stream content: %s", err)
				} else {
					if _, err = os.Stdout.Write(contentBytes); err != nil {
						u.logger.Errorf("Error writing to Stdout: %s", err)
					}
				}
			case smsg.Stop:
				u.tmb.Kill(fmt.Errorf("Received shell quit stream message."))
				return
			default:
				u.logger.Errorf("unhandled stream type: %s", streamMessage.Type)
			}
		}
	}
}

// Reads from StdIn and pushes to an input channel
func (u *UnixShell) readStdIn() {
	// switch stdin into 'raw' mode
	// https://pkg.go.dev/golang.org/x/term#pkg-overview
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		u.logger.Errorf("Error switching std to raw mode: %s", err)
		return
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	b := make([]byte, 1)

	for {
		select {
		case <-u.tmb.Dying():
			return
		default:
			n, err := os.Stdin.Read(b)
			if err != nil || n != 1 {
				u.tmb.Kill(fmt.Errorf("Error reading last keypress from Stdin: %s", err))
				return
			}

			u.stdInChan <- b[0]
		}
	}
}

// processes input channel by debouncing all keypresses within a time interval
func (u *UnixShell) processStdIn() {
	inputBuf := make([]byte, InputBufferSize)

	for {
		select {
		case <-u.tmb.Dying():
			return
		case b := <-u.stdInChan:
			inputBuf = append(inputBuf, b)
		case <-time.After(InputDebounceTime):
			if len(inputBuf) >= 1 {
				// Send all accumulated keypresses in a shellInput data message
				shellInputDataMessage := bzshell.ShellInputMessage{
					Data: inputBuf,
				}
				u.sendOutputMessage(bzshell.ShellInput, shellInputDataMessage)

				// clear the input buffer by slicing it to size 0 which will still
				// keep memory allocated for the underlying capacity of the slice
				inputBuf = inputBuf[:0]
			}
		}
	}
}

func (u *UnixShell) sendTerminalSize() {
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err != nil {
		u.logger.Errorf("Failed to get current terminal size %s", err)
	} else {
		shellResizeMessage := bzshell.ShellResizeMessage{
			Rows: uint32(h),
			Cols: uint32(w),
		}
		u.sendOutputMessage(bzshell.ShellResize, shellResizeMessage)
	}
}

// Captures any terminal resize events using the SIGWINCH signal and send the
// new terminal size
func (u *UnixShell) listenForTerminalSizeChanges() {
	ch := make(chan os.Signal, 1)
	sig := unix.SIGWINCH

	signal.Notify(ch, sig)
	go func() {
		for {
			select {
			case <-u.tmb.Dying():
				signal.Reset(sig)
				close(ch)
				return
			case <-ch:
				u.sendTerminalSize()
			}
		}
	}()
}
