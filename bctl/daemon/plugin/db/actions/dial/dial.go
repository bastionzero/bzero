package dial

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"time"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/db/actions/dial"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize = 64 * 1024
)

type DialAction struct {
	logger    *logger.Logger
	tmb       *tomb.Tomb
	requestId string

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage

	closed bool
}

func New(logger *logger.Logger,
	requestId string) (*DialAction, chan plugin.ActionWrapper) {

	stream := &DialAction{
		logger:    logger,
		requestId: requestId,

		outputChan:      make(chan plugin.ActionWrapper, 10),
		streamInputChan: make(chan smsg.StreamMessage, 30),
	}

	return stream, stream.outputChan
}

func (d *DialAction) Start(tmb *tomb.Tomb, lconn *net.TCPConn) error {
	d.tmb = tmb

	// Build and send the action payload to start the tcp connection on the agent
	payload := dial.DialActionPayload{
		RequestId: d.requestId,
	}
	d.sendOutputMessage(dial.DialStart, payload)

	// Listen to stream messages coming from the agent, and forward to our local connection
	go func() {
		for {
			select {
			case <-tmb.Dying():
				return
			case data := <-d.streamInputChan:
				if d.closed {
					return
				}

				switch smsg.StreamType(data.Type) {
				case smsg.DbStream:
					if contentBytes, err := base64.StdEncoding.DecodeString(data.Content); err != nil {
						d.logger.Errorf("could not decode db stream content: %s", err)
					} else {
						go func() {
							time.Sleep(time.Millisecond)
							lconn.Write(contentBytes) // did you know this blocks forever if you write too fast to it? yeah.
						}()
					}
				case smsg.DbStreamEnd:

					// The agent has closed the connection, close the local connection as well
					d.logger.Info("remote tcp connection has been closed, closing local tcp connection")
					d.closed = true
					lconn.Close()

					return
				default:
					d.logger.Errorf("unhandled stream type: %s", data.Type)
				}
			}
		}
	}()

	go func() {
		defer close(d.outputChan)
		defer lconn.Close()

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
				payload := dial.DialActionPayload{
					RequestId: d.requestId,
				}
				d.sendOutputMessage(dial.DialStop, payload)

				// tell our agent message listener to stop processing incoming stream messages
				d.closed = true
				return
			} else {

				// Build and send whatever we get from the local tcp connection to the agent
				dataToSend := base64.StdEncoding.EncodeToString(buf[:n])
				payload := dial.DialInputActionPayload{
					RequestId:      d.requestId,
					SequenceNumber: sequenceNumber,
					Data:           dataToSend,
				}
				d.sendOutputMessage(dial.DialInput, payload)

				sequenceNumber += 1
			}
		}
	}()

	return nil
}

func (d *DialAction) sendOutputMessage(action dial.DialSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	d.outputChan <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}

}

func (d *DialAction) closeAction() {
	d.closed = true

	// this signals to the parent plugin that we're done with the action
	close(d.outputChan)
}

func (d *DialAction) ReceiveKeysplitting(wrappedAction plugin.ActionWrapper) {
	if wrappedAction.Action == string(dial.DialStop) {
		d.closed = true
	}
}

func (d *DialAction) ReceiveStream(smessage smsg.StreamMessage) {
	d.logger.Debugf("Dial action received %v stream, message count: %d", smessage.Type, len(d.streamInputChan)+1)
	d.streamInputChan <- smessage
}
