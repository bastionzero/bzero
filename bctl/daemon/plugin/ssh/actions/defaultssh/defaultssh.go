package defaultssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	InputBufferSize   = 8 * 1024
	InputDebounceTime = 5 * time.Millisecond
)

type DefaultSsh struct {
	logger *logger.Logger
	tmb    *tomb.Tomb

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage

	// channel where we push each individual keypress byte from StdIn
	stdInChan chan []byte

	targetUser   string
	identityFile string
	publicKey    string
}

func New(logger *logger.Logger, targetUser string, identityFile string, publicKey string) (*DefaultSsh, chan plugin.ActionWrapper) {

	shellAction := &DefaultSsh{
		logger: logger,

		outputChan:      make(chan plugin.ActionWrapper, 10),
		streamInputChan: make(chan smsg.StreamMessage, 30),
		stdInChan:       make(chan []byte, InputBufferSize),

		targetUser:   targetUser,
		identityFile: identityFile,
		publicKey:    publicKey,
	}

	return shellAction, shellAction.outputChan
}

func (d *DefaultSsh) Start(tmb *tomb.Tomb) error {
	d.tmb = tmb

	// FIXME: sub in identity file
	//publicKey, _ := GenerateKeys(d.identityFile)
	d.logger.Errorf("oh hi mark it's a %s", d.publicKey)

	sshOpenMessage := bzssh.SshOpenMessage{
		TargetUser: d.targetUser,
		PublicKey:  []byte(d.publicKey),
	}
	d.sendOutputMessage(bzssh.SshOpen, sshOpenMessage)

	// listen to stream messages on input chan and write to stdout
	go d.handleStreamMessages()

	// reading Stdin in raw mode and forward keypresses after debouncing
	go d.readStdIn()
	go d.sendStdIn()

	return nil
}

func (d *DefaultSsh) ReceiveStream(smessage smsg.StreamMessage) {
	d.logger.Debugf("Default ssh received %+v stream, message count: %d", smessage.Type, len(d.streamInputChan)+1)
	d.streamInputChan <- smessage
}

func (d *DefaultSsh) handleStreamMessages() {
	for {
		select {
		case <-d.tmb.Dying():
			return
		case streamMessage := <-d.streamInputChan:
			// process the incoming stream messages
			switch smsg.StreamType(streamMessage.Type) {
			case smsg.StdOut:
				if contentBytes, err := base64.StdEncoding.DecodeString(streamMessage.Content); err != nil {
					d.logger.Errorf("Error decoding ssh StdOut stream content: %s", err)
				} else {
					d.logger.Infof("Bad cat just wrote %s", contentBytes)
					if _, err = os.Stdout.Write(contentBytes); err != nil {
						d.logger.Errorf("Error writing to Stdout: %s", err)
					}
				}
			case smsg.Stop:
				d.tmb.Kill(fmt.Errorf("received ssh quit stream message"))
				return
			case smsg.Error:
				// TODO: revisit
				d.tmb.Kill(fmt.Errorf("received an error from the agent"))
				return
			default:
				d.logger.Errorf("unhandled stream type: %s", streamMessage.Type)
			}
		}
	}
}

// Reads from StdIn and pushes to an input channel
func (d *DefaultSsh) readStdIn() {

	b := make([]byte, InputBufferSize)
	b = b[:0]

	d.logger.Infof("Oh I'm running all right $$$$$$")
	for {
		select {
		case <-d.tmb.Dying():
			return
		default:
			n, err := os.Stdin.Read(b)
			if err != nil {
				d.tmb.Kill(fmt.Errorf("error reading last keypress from Stdin: %s", err))
				return
			}
			if n > 0 {
				d.logger.Infof("$$$ I'm a good dog and I did the reading: %s $$$", b)

				d.stdInChan <- b[:n]
				b = b[:0]
			}
		}
	}
}

// processes input channel by debouncing all keypresses within a time interval
func (d *DefaultSsh) sendStdIn() {
	inputBuf := make([]byte, InputBufferSize)
	inputBuf = inputBuf[:0]

	for {
		select {
		case <-d.tmb.Dying():
			return
		case b := <-d.stdInChan:
			inputBuf = append(inputBuf, b...)
		case <-time.After(InputDebounceTime):
			if len(inputBuf) >= 1 {
				// Send all accumulated keypresses in a shellInput data message
				sshInputDataMessage := bzssh.SshInputMessage{
					Data: inputBuf,
				}
				d.sendOutputMessage(bzssh.SshInput, sshInputDataMessage)

				// clear the input buffer by slicing it to size 0 which will still
				// keep memory allocated for the underlying capacity of the slice
				inputBuf = inputBuf[:0]
			}
		}
	}
}

func (d *DefaultSsh) sendOutputMessage(action bzssh.SshSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	d.outputChan <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}
}
