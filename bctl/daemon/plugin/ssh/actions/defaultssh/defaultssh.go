package defaultssh

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"time"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// TODO: revisit
const (
	chunkSize     = 64 * 1024
	writeDeadline = 5 * time.Second
)

type DefaultSshAction struct {
	logger *logger.Logger
	tmb    *tomb.Tomb

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage

	targetUser string

	closed bool
}

// FIXME: need to get a targetUser as input
func New(logger *logger.Logger) (*DefaultSshAction, chan plugin.ActionWrapper) {

	stream := &DefaultSshAction{
		logger: logger,

		outputChan:      make(chan plugin.ActionWrapper, 10),
		streamInputChan: make(chan smsg.StreamMessage, 30),
	}

	return stream, stream.outputChan
}

func (d *DefaultSshAction) Start(tmb *tomb.Tomb, lconn *net.TCPConn) error {
	d.tmb = tmb

	// Build and send the action payload to start the tcp connection on the agent
	payload := bzssh.SshOpenMessage{
		TargetUser: d.targetUser,
	}
	d.sendOutputMessage(bzssh.SshOpen, payload)

	// Listen to stream messages coming from the agent, and forward to our local connection
	go func() {
		defer lconn.Close()

		// variables for ensuring we receive stream messages in order
		expectedSequenceNumber := 0
		streamMessages := make(map[int]smsg.StreamMessage)

		for {
			select {
			case <-tmb.Dying():
				return
			case data := <-d.streamInputChan:
				if d.closed {
					return
				}
				streamMessages[data.SequenceNumber] = data

				// process the incoming stream messages *in order*
				for streamMessage, ok := streamMessages[expectedSequenceNumber]; ok; streamMessage, ok = streamMessages[expectedSequenceNumber] {
					if streamMessage.Type == smsg.Stream {
						if contentBytes, err := base64.StdEncoding.DecodeString(streamMessage.Content); err != nil {
							d.logger.Errorf("could not decode ssh content: %s", err)
						} else {
							// Set a deadline for the write so we don't block forever
							lconn.SetWriteDeadline(time.Now().Add(writeDeadline))
							if _, err := lconn.Write(contentBytes); err != nil {
								d.logger.Errorf("Error writing to local TCP connection: %s", err)
								d.closed = true
								return
							}

							if !streamMessage.More {
								// since there's no more stream coming, close the local connection
								d.logger.Info("remote tcp connection has been closed, closing local tcp connection")
								d.closed = true
								return
							}
						}
					} else {
						d.logger.Errorf("unhandled stream type: %s", streamMessage.Type)
					}

					// remove the message we've already processed
					delete(streamMessages, expectedSequenceNumber)

					// increment our sequence number
					expectedSequenceNumber += 1
				}
			}
		}
	}()

	go func() {
		defer close(d.outputChan)

		// listen to messages coming from the local tcp connection and sends them to the agent
		buf := make([]byte, chunkSize)
		sequenceNumber := 0

		for {
			if n, err := lconn.Read(buf); err != nil {
				if d.closed {
					return
				}

				// print our error message
				if err == io.EOF {
					d.logger.Info("local tcp connection has been closed")
				} else {
					d.logger.Errorf("error reading from local tcp connection: %s", err)
				}

				// let the agent know we need to stop
				payload := bzssh.SshCloseMessage{}
				d.sendOutputMessage(bzssh.SshClose, payload)
				return

			} else {
				// Build and send whatever we get from the local tcp connection to the agent
				dataToSend := base64.StdEncoding.EncodeToString(buf[:n])
				payload := bzssh.SshInputMessage{
					SequenceNumber: sequenceNumber,
					Data:           dataToSend,
				}
				d.sendOutputMessage(bzssh.SshInput, payload)

				sequenceNumber += 1
			}
		}
	}()

	return nil
}

func (d *DefaultSshAction) sendOutputMessage(action bzssh.SshSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	d.outputChan <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}
}

func (d *DefaultSshAction) ReceiveKeysplitting(wrappedAction plugin.ActionWrapper) {
	if wrappedAction.Action == string(bzssh.SshClose) {
		d.closed = true
	}
}

func (d *DefaultSshAction) ReceiveStream(smessage smsg.StreamMessage) {
	d.logger.Debugf("Default ssh received %v stream, message count: %d", smessage.Type, len(d.streamInputChan)+1)
	d.streamInputChan <- smessage
}
