package opaquessh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/ssh/authorizedkeys"
	"bastionzero.com/bctl/v1/bzerolib/bzio"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize     = 64 * 1024
	writeDeadline = 5 * time.Second
	rsaKeyPath    = "/etc/ssh/ssh_host_rsa_key.pub"
)

type OpaqueSsh struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	// channel for letting the plugin know we're done
	doneChan chan struct{}

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	remoteConnection *net.TCPConn
	authorizedKeys   authorizedkeys.IAuthorizedKeys

	fileIo bzio.BzFileIo
}

func New(logger *logger.Logger, doneChan chan struct{}, ch chan smsg.StreamMessage, conn *net.TCPConn, authKeys authorizedkeys.IAuthorizedKeys, fileIo bzio.BzFileIo) *OpaqueSsh {

	return &OpaqueSsh{
		logger:           logger,
		doneChan:         doneChan,
		streamOutputChan: ch,
		remoteConnection: conn,
		authorizedKeys:   authKeys,
		fileIo:           fileIo,
	}
}

func (s *OpaqueSsh) Kill() {
	s.tmb.Kill(nil)
	if s.remoteConnection != nil {
		(*s.remoteConnection).Close()
	}
	s.tmb.Wait()
}

func (s *OpaqueSsh) Receive(action string, actionPayload []byte) ([]byte, error) {

	switch bzssh.SshSubAction(action) {
	case bzssh.SshOpen:
		var openRequest bzssh.SshOpenMessage
		if err := json.Unmarshal(actionPayload, &openRequest); err != nil {
			return nil, fmt.Errorf("malformed opaque ssh action payload %s", string(actionPayload))
		} else if err = s.authorizedKeys.Add(string(openRequest.PublicKey)); err != nil {
			return nil, err
		}

		return s.start(openRequest, action)

	case bzssh.SshInput:
		// Deserialize the action payload, the only action passed is input
		var inputRequest bzssh.SshInputMessage
		if err := json.Unmarshal(actionPayload, &inputRequest); err != nil {
			return nil, fmt.Errorf("unable to unmarshal opaque ssh input message: %s", err)
		}

		// Set a deadline for the write so we don't block forever
		(*s.remoteConnection).SetWriteDeadline(time.Now().Add(writeDeadline))
		if _, err := (*s.remoteConnection).Write(inputRequest.Data); !s.tmb.Alive() {
			return []byte{}, nil
		} else if err != nil {
			s.logger.Errorf("error writing to local TCP connection: %s", err)
			s.Kill()
		}

	case bzssh.SshClose:
		// Deserialize the action payload
		var closeRequest bzssh.SshCloseMessage
		if jerr := json.Unmarshal(actionPayload, &closeRequest); jerr != nil {
			// not a fatal error, we can still just close without a reason
			s.logger.Errorf("unable to unmarshal opaque ssh close message: %s", jerr)
		}

		s.logger.Infof("Ending TCP connection because we received this close message from daemon: %s", closeRequest.Reason)
		s.Kill()

		return actionPayload, nil
	default:
		return nil, fmt.Errorf("unhandled stream action: %s", action)
	}

	return []byte{}, nil
}

func (s *OpaqueSsh) start(openRequest bzssh.SshOpenMessage, action string) ([]byte, error) {
	s.streamMessageVersion = openRequest.StreamMessageVersion
	s.logger.Debugf("Setting stream message version: %s", s.streamMessageVersion)

	// send an RSA key to the daemon if we can find it
	if rsaKey, err := s.fileIo.ReadFile(rsaKeyPath); err != nil {
		s.logger.Errorf("unable to read key file at %s: %s", rsaKeyPath, err)
	} else {
		s.sendStreamMessage(0, smsg.Data, false, rsaKey)
	}

	// Setup a go routine to listen for messages coming from this local connection and send to daemon
	s.tmb.Go(func() error {
		defer close(s.doneChan)

		sequenceNumber := 1
		buff := make([]byte, chunkSize)

		for {
			select {
			case <-s.tmb.Dying():
				s.logger.Errorf("tomb was killed. Stopping...")
				return nil
			default:
				// this line blocks until it reads output or error
				if n, err := (*s.remoteConnection).Read(buff); !s.tmb.Alive() {
					return nil
				} else if err != nil {
					if err == io.EOF {
						s.logger.Errorf("connection closed (EOF)")
						// Let our daemon know that we have got the error and we need to close the connection
						s.sendStreamMessage(sequenceNumber, smsg.StdOut, false, buff[:n])
					} else {
						s.logger.Errorf("failed to read from tcp connection: %s", err)
						s.sendStreamMessage(sequenceNumber, smsg.Error, false, buff[:n])
					}
					return err
				} else {
					s.logger.Debugf("Sending %d bytes from local tcp connection to daemon", n)

					// Now send this to daemon
					s.sendStreamMessage(sequenceNumber, smsg.StdOut, true, buff[:n])

					sequenceNumber += 1
				}
			}
		}
	})

	// Update our remote connection
	return []byte{}, nil
}

func (s *OpaqueSsh) sendStreamMessage(sequenceNumber int, streamType smsg.StreamType, more bool, contentBytes []byte) {
	s.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion:  s.streamMessageVersion,
		SequenceNumber: sequenceNumber,
		Action:         string(bzssh.OpaqueSsh),
		Type:           streamType,
		More:           more,
		Content:        base64.StdEncoding.EncodeToString(contentBytes),
	}
}
