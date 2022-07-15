package opaquessh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	gossh "golang.org/x/crypto/ssh"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
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

	stdIo io.ReadWriter

	identityFile bzssh.IIdentityFile
	knownHosts   bzssh.IKnownHosts
}

func New(
	logger *logger.Logger,
	outboxQueue chan plugin.ActionWrapper,
	doneChan chan struct{},
	stdIo io.ReadWriter,
	identityFile bzssh.IIdentityFile,
	knownHosts bzssh.IKnownHosts,
) *OpaqueSsh {

	return &OpaqueSsh{
		logger:       logger,
		outboxQueue:  outboxQueue,
		doneChan:     doneChan,
		stdInChan:    make(chan []byte, InputBufferSize),
		stdIo:        stdIo,
		identityFile: identityFile,
		knownHosts:   knownHosts,
	}
}

func (s *OpaqueSsh) Done() <-chan struct{} {
	return s.doneChan
}

func (s *OpaqueSsh) Kill() {
	s.tmb.Kill(nil)
}

func (s *OpaqueSsh) Start() error {

	// if we already have a private key, use it. Otherwise, create a new one
	// NOTE: it is technically possible for this to create a one-time race if two SSH processes
	// are kicked off *and* the user just logged in. However this is unlikely and can be resolved
	// if/when we upgrade the SSH architecture
	_, publicKey, err := bzssh.SetUpKeys(s.identityFile)
	if err != nil {
		return fmt.Errorf("failed to set up ssh keypair: %s", err)
	}

	sshOpenMessage := bzssh.SshOpenMessage{
		PublicKey:            []byte(publicKey),
		StreamMessageVersion: smsg.CurrentSchema,
	}

	s.sendOutputMessage(bzssh.SshOpen, sshOpenMessage)

	go func() {
		defer close(s.doneChan)
		<-s.tmb.Dying()
	}()

	s.tmb.Go(func() error {
		b := make([]byte, InputBufferSize)

		for {
			select {
			case <-s.tmb.Dying():
				return nil
			default:
				if n, err := s.stdIo.Read(b); !s.tmb.Alive() {
					return nil
				} else if err != nil {
					if err == io.EOF {
						s.sendOutputMessage(bzssh.SshClose, bzssh.SshCloseMessage{Reason: endedByUser})
						return fmt.Errorf("finished reading from stdin")
					}
					return fmt.Errorf("error reading from Stdin: %s", err)
				} else if n > 0 {
					s.logger.Debugf("Read %d bytes from local SSH", n)
					s.sendSshInputMessage(b[:n])
				}
			}
		}
	})

	return nil
}

func (s *OpaqueSsh) ReceiveStream(smessage smsg.StreamMessage) {
	s.logger.Debugf("opaque ssh received %+v stream", smessage.Type)
	switch smsg.StreamType(smessage.Type) {
	case smsg.StdOut:
		if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
			s.logger.Errorf("Error decoding ssh StdOut stream content: %s", err)
		} else {
			if _, err = s.stdIo.Write(contentBytes); err != nil {
				s.logger.Errorf("Error writing to Stdout: %s", err)
			}
			if !smessage.More {
				s.tmb.Kill(fmt.Errorf("received ssh close stream message"))
				return
			}
		}
	case smsg.Error:
		s.tmb.Kill(fmt.Errorf("received an error from the agent"))
		return
	// a ready message from the agent will contain the host key we can use
	case smsg.Data:
		if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
			s.logger.Errorf("error decoding ssh ready stream content: %s", err)
		} else if parsedKey, _, _, _, err := gossh.ParseAuthorizedKey(contentBytes); err != nil {
			s.logger.Errorf("could not unmarshal public key data: %s", err)
		} else {
			s.knownHosts.AddHostKeyPublic(parsedKey)
		}
	default:
		s.logger.Errorf("unhandled stream type: %s", smessage.Type)
	}
}

func (s *OpaqueSsh) sendSshInputMessage(bs []byte) {
	// Send all accumulated input in an sshInput data message
	sshInputDataMessage := bzssh.SshInputMessage{
		Data: bs,
	}
	s.sendOutputMessage(bzssh.SshInput, sshInputDataMessage)
}

func (s *OpaqueSsh) sendOutputMessage(action bzssh.SshSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	s.outboxQueue <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}
}
