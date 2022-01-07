package dial

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/db/action/dial"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type DialAction struct {
	logger    *logger.Logger
	requestId string

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage
	ksInputChan     chan plugin.ActionWrapper

	sequenceNumber  int
	localConnection *net.TCPConn
}

func New(logger *logger.Logger,
	requestId string) (*DialAction, chan plugin.ActionWrapper) {

	stream := &DialAction{
		logger:    logger,
		requestId: requestId,

		outputChan:      make(chan plugin.ActionWrapper, 10),
		streamInputChan: make(chan smsg.StreamMessage, 10),
		ksInputChan:     make(chan plugin.ActionWrapper, 10),

		sequenceNumber: 0,
	}

	return stream, stream.outputChan
}

func (s *DialAction) Start(tmb *tomb.Tomb, lconn *net.TCPConn) error {
	// this action ends at the end of this function, in order to signal that to the parent plugin,
	// we close the output channel which will close the go routine listening on it
	defer close(s.outputChan)

	// Set our local connection
	s.localConnection = lconn

	// Build the action payload
	payload := dial.DialActionPayload{
		RequestId: s.requestId,
	}

	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	s.outputChan <- plugin.ActionWrapper{
		Action:        string(dial.DialStart),
		ActionPayload: payloadBytes,
	}

	// Listen to stream messages coming from bastion, and forward to our local connection
	go func() {
		for {
			select {
			case data := <-s.streamInputChan:
				switch smsg.StreamType(data.Type) {
				case smsg.DbOut:
					contentBytes, _ := base64.StdEncoding.DecodeString(data.Content)

					_, err := lconn.Write(contentBytes)
					if err != nil {
						s.logger.Errorf("write failed: %s", err)
					}
				case smsg.DbAgentClose:
					// The agent has closed the connection, close the local connection as well
					s.logger.Info("remote tcp connection has been closed, closing local tcp connection")
					lconn.Close()
				default:
					s.logger.Errorf("unhandled stream type: %s", data.Type)
				}
			}
		}
	}()

	// Keep looping till we hit EOF
	tmp := make([]byte, 0xffff)
	for {
		n, err := lconn.Read(tmp)
		if err == io.EOF {
			// Tell the agent to stop the dial session
			s.logger.Info("local tcp connection has been closed")

			return nil
		}
		if err != nil {
			s.logger.Errorf("Read failed '%s'\n", err)
			// Tell the agent to stop the dial session
			return nil
		}

		buff := tmp[:n]

		dataToSend := base64.StdEncoding.EncodeToString(buff)

		// Build the action payload
		payload := dial.DialInputActionPayload{
			RequestId:      s.requestId,
			SequenceNumber: s.sequenceNumber,
			Data:           dataToSend,
		}

		// Send payload to plugin output queue
		payloadBytes, _ := json.Marshal(payload)
		s.outputChan <- plugin.ActionWrapper{
			Action:        string(dial.DialInput),
			ActionPayload: payloadBytes,
		}

		s.sequenceNumber += 1
	}
}

func (s *DialAction) ReceiveKeysplitting(wrappedAction plugin.ActionWrapper) {
	s.ksInputChan <- wrappedAction
}

func (s *DialAction) ReceiveStream(smessage smsg.StreamMessage) {
	s.logger.Debugf("Stream action received %v stream", smessage.Type)
	s.streamInputChan <- smessage
}
