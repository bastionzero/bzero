package defaultssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	InputBufferSize   = 8 * 1024
	InputDebounceTime = 5 * time.Millisecond
)

type DefaultSsh struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	outputChan chan plugin.ActionWrapper // plugin's output queue
	doneChan   chan struct{}

	// channel where we push from StdIn
	stdInChan chan []byte

	identityFile string
}

func New(logger *logger.Logger, outboxQueue chan plugin.ActionWrapper, doneChan chan struct{}, identityFile string) *DefaultSsh {

	return &DefaultSsh{
		logger:       logger,
		outputChan:   outboxQueue,
		doneChan:     doneChan,
		stdInChan:    make(chan []byte, InputBufferSize),
		identityFile: identityFile,
	}
}

func (d *DefaultSsh) Done() <-chan struct{} {
	return d.doneChan
}

func (d *DefaultSsh) Kill() {
	d.tmb.Kill(nil)
}

func (d *DefaultSsh) Start() error {

	var privateKey, publicKey []byte

	// if we already have a private key, use it. Otherwise, create a new one
	if publicKeyRsa, err := readPublicKeyRsa(d.identityFile); err == nil {
		if publicKey, err = generatePublicKey(publicKeyRsa); err != nil {
			return fmt.Errorf("error decoding temporary public key: %s", err)
		} else {
			d.logger.Debugf("using existing temporary keys")
		}
	} else {
		d.logger.Debugf("generating new temporary keys")
		privateKey, publicKey, err = GenerateKeys()
		if err != nil {
			return fmt.Errorf("error generating temporary keys: %s", err)
		} else if err := os.WriteFile(d.identityFile, privateKey, 0600); err != nil {
			return fmt.Errorf("error writing temporary private key: %s", err)
		}
	}

	sshOpenMessage := ssh.SshOpenMessage{
		PublicKey:            []byte(publicKey),
		StreamMessageVersion: smsg.CurrentSchema,
	}
	d.sendOutputMessage(ssh.SshOpen, sshOpenMessage)

	// reading Stdin in raw mode and forward keypresses after debouncing
	go d.readStdIn()
	go d.sendStdIn()

	return nil

}

func (d *DefaultSsh) ReceiveStream(smessage smsg.StreamMessage) {
	d.logger.Debugf("Default ssh received %+v stream", smessage.Type)
	switch smsg.StreamType(smessage.Type) {
	case smsg.StdOut:
		if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
			d.logger.Errorf("Error decoding ssh StdOut stream content: %s", err)
		} else {
			if _, err = os.Stdout.Write(contentBytes); err != nil {
				d.logger.Errorf("Error writing to Stdout: %s", err)
			}
			if !smessage.More {
				d.tmb.Kill(fmt.Errorf("received ssh close stream message"))
				close(d.doneChan)
				return
			}
		}
	case smsg.Error:
		d.tmb.Kill(fmt.Errorf("received an error from the agent"))
		return
	default:
		d.logger.Errorf("unhandled stream type: %s", smessage.Type)
	}
}

// Reads from StdIn and pushes to an input channel
func (d *DefaultSsh) readStdIn() {

	b := make([]byte, InputBufferSize)

	for {
		select {
		case <-d.tmb.Dying():
			d.logger.Errorf("oh I'm dying now")
			return
		default:
			n, err := os.Stdin.Read(b)
			if err != nil {
				if err == io.EOF {
					d.tmb.Kill(fmt.Errorf("finished reading from stdin"))
					close(d.doneChan)
					d.sendOutputMessage(ssh.SshClose, ssh.SshCloseMessage{Reason: "SSH session ended"})
					return
				}
				d.tmb.Kill(fmt.Errorf("error reading from Stdin: %s", err))
				return
			}
			if n > 0 {
				d.logger.Debugf("Read %d bytes from local SSH", n)
				d.stdInChan <- b[:n]
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
				// Send all accumulated input in an sshInput data message
				sshInputDataMessage := ssh.SshInputMessage{
					Data: inputBuf,
				}
				d.sendOutputMessage(ssh.SshInput, sshInputDataMessage)

				// clear the input buffer by slicing it to size 0 which will still
				// keep memory allocated for the underlying capacity of the slice
				inputBuf = inputBuf[:0]
			}
		}
	}
}

func (d *DefaultSsh) sendOutputMessage(action ssh.SshSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	d.outputChan <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}
}
