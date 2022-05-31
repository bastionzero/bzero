package opaquessh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/bzio"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	InputBufferSize = int(64 * 1024)
	endedByUser     = "SSH session ended"
)

type OpaqueSsh struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	outboxQueue chan plugin.ActionWrapper
	doneChan    chan struct{}

	// channel where we push from StdIn
	stdInChan chan []byte

	identityFile string

	filIo bzio.BzFileIo
	stdIo io.ReadWriter
}

func New(
	logger *logger.Logger,
	outboxQueue chan plugin.ActionWrapper,
	doneChan chan struct{},
	identityFile string,
	filIo bzio.BzFileIo,
	stdIo io.ReadWriter,
) *OpaqueSsh {

	return &OpaqueSsh{
		logger:       logger,
		outboxQueue:  outboxQueue,
		doneChan:     doneChan,
		stdInChan:    make(chan []byte, InputBufferSize),
		identityFile: identityFile,
		filIo:        filIo,
		stdIo:        stdIo,
	}
}

func (d *OpaqueSsh) Done() <-chan struct{} {
	return d.doneChan
}

func (d *OpaqueSsh) Kill() {
	d.tmb.Kill(nil)
}

func (d *OpaqueSsh) Start() error {

	var privateKey, publicKey []byte

	// if we already have a private key, use it. Otherwise, create a new one
	// NOTE: it is technically possible for this to create a one-time race if two SSH processes
	// are kicked off *and* the user just logged in. However this is unlikely and can be resolved
	// if/when we upgrade the SSH architecture
	if publicKeyRsa, err := readPublicKeyRsa(d.identityFile, d.filIo); err == nil {
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
		} else if err := d.filIo.WriteFile(d.identityFile, privateKey, 0600); err != nil {
			return fmt.Errorf("error writing temporary private key: %s", err)
		}
	}

	sshOpenMessage := ssh.SshOpenMessage{
		PublicKey:            []byte(publicKey),
		StreamMessageVersion: smsg.CurrentSchema,
	}

	d.sendOutputMessage(ssh.SshOpen, sshOpenMessage)

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
				if n, err := d.stdIo.Read(b); !d.tmb.Alive() {
					return nil
				} else if err != nil {
					if err == io.EOF {
						d.sendOutputMessage(ssh.SshClose, ssh.SshCloseMessage{Reason: endedByUser})
						return fmt.Errorf("finished reading from stdin")
					}
					return fmt.Errorf("error reading from Stdin: %s", err)
				} else if n > 0 {
					d.logger.Debugf("Read %d bytes from local SSH", n)
					d.sendSshInputMessage(b[:n])
				}
			}
		}
	})

	return nil
}

func (d *OpaqueSsh) ReceiveStream(smessage smsg.StreamMessage) {
	d.logger.Debugf("Default ssh received %+v stream", smessage.Type)
	switch smsg.StreamType(smessage.Type) {
	case smsg.StdOut:
		if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
			d.logger.Errorf("Error decoding ssh StdOut stream content: %s", err)
		} else {
			if _, err = d.stdIo.Write(contentBytes); err != nil {
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

func (d *OpaqueSsh) sendSshInputMessage(bs []byte) {
	// Send all accumulated input in an sshInput data message
	sshInputDataMessage := ssh.SshInputMessage{
		Data: bs,
	}
	d.sendOutputMessage(ssh.SshInput, sshInputDataMessage)
}

func (d *OpaqueSsh) sendOutputMessage(action ssh.SshSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	d.outboxQueue <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}
}
