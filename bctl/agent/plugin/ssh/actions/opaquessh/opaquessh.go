package opaquessh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize     = 64 * 1024
	writeDeadline = 5 * time.Second
)

type AuthorizedKeysInterface interface {
	Add(pubkey string) error
}

type OpaqueSsh struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	// channel for letting the plugin know we're done
	doneChan chan struct{}

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	remoteConnection *net.TCPConn
	authorizedKeys   AuthorizedKeysInterface
}

func New(logger *logger.Logger, doneChan chan struct{}, ch chan smsg.StreamMessage, conn *net.TCPConn, authKeys AuthorizedKeysInterface) *OpaqueSsh {

	return &OpaqueSsh{
		logger:           logger,
		doneChan:         doneChan,
		streamOutputChan: ch,
		remoteConnection: conn,
		authorizedKeys:   authKeys,
	}
}

func (d *OpaqueSsh) Kill() {
	d.tmb.Kill(nil)
	if d.remoteConnection != nil {
		(*d.remoteConnection).Close()
	}
	d.tmb.Wait()
}

func (d *OpaqueSsh) Receive(action string, actionPayload []byte) ([]byte, error) {

	switch ssh.SshSubAction(action) {
	case ssh.SshOpen:
		var openRequest ssh.SshOpenMessage
		if err := json.Unmarshal(actionPayload, &openRequest); err != nil {
			return nil, fmt.Errorf("malformed default SSH action payload %s", string(actionPayload))
		} else if err = d.authorizedKeys.Add(string(openRequest.PublicKey)); err != nil {
			return nil, err
		}

		return d.start(openRequest, action)
	case ssh.SshInput:

		// Deserialize the action payload, the only action passed is input
		var inputRequest ssh.SshInputMessage
		if err := json.Unmarshal(actionPayload, &inputRequest); err != nil {
			return nil, fmt.Errorf("unable to unmarshal default SSH input message: %s", err)
		}

		// Set a deadline for the write so we don't block forever
		(*d.remoteConnection).SetWriteDeadline(time.Now().Add(writeDeadline))
		if _, err := (*d.remoteConnection).Write(inputRequest.Data); !d.tmb.Alive() {
			return []byte{}, nil
		} else if err != nil {
			d.logger.Errorf("error writing to local TCP connection: %s", err)
			d.Kill()
		}

	case ssh.SshClose:
		// Deserialize the action payload
		var closeRequest ssh.SshCloseMessage
		if jerr := json.Unmarshal(actionPayload, &closeRequest); jerr != nil {
			// not a fatal error, we can still just close without a reason
			d.logger.Errorf("unable to unmarshal default SSH close message: %s", jerr)
		}

		d.logger.Infof("Ending TCP connection because we received this close message from daemon: %s", closeRequest.Reason)
		d.Kill()

		return actionPayload, nil
	default:
		return nil, fmt.Errorf("unhandled stream action: %s", action)
	}

	return []byte{}, nil
}

func (d *OpaqueSsh) start(openRequest ssh.SshOpenMessage, action string) ([]byte, error) {
	d.streamMessageVersion = openRequest.StreamMessageVersion
	d.logger.Debugf("Setting stream message version: %s", d.streamMessageVersion)

	// Setup a go routine to listen for messages coming from this local connection and send to daemon
	d.tmb.Go(func() error {
		defer close(d.doneChan)

		sequenceNumber := 0
		buff := make([]byte, chunkSize)

		for {
			select {
			case <-d.tmb.Dying():
				d.logger.Errorf("got killed")
				return nil
			default:
				// this line blocks until it reads output or error
				if n, err := (*d.remoteConnection).Read(buff); !d.tmb.Alive() {
					return nil
				} else if err != nil {
					if err == io.EOF {
						d.logger.Errorf("connection closed (EOF)")
						// Let our daemon know that we have got the error and we need to close the connection
						d.sendStreamMessage(sequenceNumber, smsg.StdOut, false, buff[:n])
					} else {
						d.logger.Errorf("failed to read from tcp connection: %s", err)
						d.sendStreamMessage(sequenceNumber, smsg.Error, false, buff[:n])
					}
					return err
				} else {
					//d.logger.Debugf("Sending %d bytes from local tcp connection to daemon", n)

					// Now send this to daemon
					d.sendStreamMessage(sequenceNumber, smsg.StdOut, true, buff[:n])

					sequenceNumber += 1
				}
			}
		}
	})

	// Update our remote connection
	return []byte{}, nil
}

func (d *OpaqueSsh) sendStreamMessage(sequenceNumber int, streamType smsg.StreamType, more bool, contentBytes []byte) {
	d.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion:  d.streamMessageVersion,
		SequenceNumber: sequenceNumber,
		Action:         string(ssh.OpaqueSsh),
		Type:           streamType,
		More:           more,
		Content:        base64.StdEncoding.EncodeToString(contentBytes),
	}
}
