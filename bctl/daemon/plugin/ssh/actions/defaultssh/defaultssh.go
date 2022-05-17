package defaultssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	"bastionzero.com/bctl/v1/bzerolib/services/fileservice"
	"bastionzero.com/bctl/v1/bzerolib/services/ioservice"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	InputBufferSize   = int(8 * 1024)
	InputDebounceTime = 5 * time.Millisecond
)

type DefaultSsh struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	outboxQueue chan plugin.ActionWrapper
	doneChan    chan struct{}

	// channel where we push from StdIn
	stdInChan chan []byte

	identityFile string

	fileService fileservice.FileService
	ioService   ioservice.IoService
}

func New(
	logger *logger.Logger,
	outboxQueue chan plugin.ActionWrapper,
	doneChan chan struct{},
	identityFile string,
	fileService fileservice.FileService,
	ioService ioservice.IoService,
) *DefaultSsh {

	return &DefaultSsh{
		logger:       logger,
		outboxQueue:  outboxQueue,
		doneChan:     doneChan,
		stdInChan:    make(chan []byte, InputBufferSize),
		identityFile: identityFile,
		fileService:  fileService,
		ioService:    ioService,
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
	if publicKeyRsa, err := readPublicKeyRsa(d.identityFile, d.fileService); err == nil {
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
		} else if err := d.fileService.WriteFile(d.identityFile, privateKey, 0600); err != nil {
			return fmt.Errorf("error writing temporary private key: %s", err)
		}
	}

	sshOpenMessage := ssh.SshOpenMessage{
		PublicKey:            []byte(publicKey),
		StreamMessageVersion: smsg.CurrentSchema,
	}

	d.sendOutputMessage(ssh.SshOpen, sshOpenMessage)

	// go d.sendStdIn()
	go func() {
		defer close(d.doneChan)
		<-d.tmb.Dying()
	}()

	d.tmb.Go(func() error {
		b := make([]byte, InputBufferSize)

		for {
			select {
			case <-d.tmb.Dying():
				return nil
			default:
				if n, err := d.ioService.Read(b); !d.tmb.Alive() {
					return nil
				} else if err != nil {
					if err == io.EOF {
						d.sendOutputMessage(ssh.SshClose, ssh.SshCloseMessage{Reason: "SSH session ended"})
						return fmt.Errorf("finished reading from stdin")
					}
					return fmt.Errorf("error reading from Stdin: %s", err)
				} else if n > 0 {
					d.logger.Debugf("Read %d bytes from local SSH", n)
					d.sendSshInputMessage(b[:n])
					// d.stdInChan <- b[:n]
				}
			}
		}
	})

	return nil

}

func (d *DefaultSsh) ReceiveStream(smessage smsg.StreamMessage) {
	d.logger.Debugf("Default ssh received %+v stream", smessage.Type)
	switch smsg.StreamType(smessage.Type) {
	case smsg.StdOut:
		if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
			d.logger.Errorf("Error decoding ssh StdOut stream content: %s", err)
		} else {
			if _, err = d.ioService.Write(contentBytes); err != nil {
				d.logger.Errorf("Error writing to Stdout: %s", err)
			}
			if !smessage.More {
				d.tmb.Kill(fmt.Errorf("received ssh close stream message"))
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

// processes input channel by debouncing all keypresses within a time interval
// func (d *DefaultSsh) sendStdIn() {
// 	inputBuf := []byte{}

// 	for {
// 		select {
// 		case <-d.tmb.Dying():
// 			return
// 		case b := <-d.stdInChan:
// 			inputBuf = append(inputBuf, b...)
// 			if len(inputBuf) >= InputBufferSize {
// 				d.sendSshInputMessage(inputBuf)
// 				inputBuf = inputBuf[:0]
// 			}
// 		case <-time.After(InputDebounceTime):
// 			if len(inputBuf) >= 1 {
// 				d.sendSshInputMessage(inputBuf)
// 				inputBuf = inputBuf[:0]
// 			}
// 		}
// 	}
// }

func (d *DefaultSsh) sendSshInputMessage(bs []byte) {
	// Send all accumulated input in an sshInput data message
	sshInputDataMessage := ssh.SshInputMessage{
		Data: bs,
	}
	d.sendOutputMessage(ssh.SshInput, sshInputDataMessage)
}

func (d *DefaultSsh) sendOutputMessage(action ssh.SshSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	d.outboxQueue <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}
}
