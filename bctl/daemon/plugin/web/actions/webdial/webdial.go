package webdial

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/web/actions/webdial"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"

	"gopkg.in/tomb.v2"
)

const (
	startDial  = "web/dial/start"
	dialDataIn = "web/dial/datain"
)

type WebAction struct {
	logger *logger.Logger

	requestId string

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage
	mzInputChan     chan plugin.ActionWrapper

	sequenceNumber  int
	localConnection *net.TCPConn
}

func New(logger *logger.Logger,
	requestId string) (*WebAction, chan plugin.ActionWrapper) {

	stream := &WebAction{
		logger: logger,

		requestId: requestId,

		outputChan:      make(chan plugin.ActionWrapper, 10),
		streamInputChan: make(chan smsg.StreamMessage, 10),
		mzInputChan:     make(chan plugin.ActionWrapper, 10),

		sequenceNumber: 0,
	}

	return stream, stream.outputChan
}

func (s *WebAction) Start(tmb *tomb.Tomb, lconn *net.TCPConn) error {
	// this action ends at the end of this function, in order to signal that to the parent plugin,
	// we close the output channel which will close the go routine listening on it
	defer close(s.outputChan)

	// Set our local connection
	s.localConnection = lconn

	// Build the action payload
	payload := webdial.WebDialActionPayload{
		RequestId: s.requestId,
	}

	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	s.outputChan <- plugin.ActionWrapper{
		Action:        startDial,
		ActionPayload: payloadBytes,
	}

	// Listen to stream messages coming from bastion, and forward to our local connection
	go func() {
		for {
			select {
			case data := <-s.streamInputChan:
				switch smsg.StreamType(data.Type) {
				case smsg.WebOut:
					contentBytes, _ := base64.StdEncoding.DecodeString(data.Content)

					_, err := lconn.Write(contentBytes)
					if err != nil {
						s.logger.Errorf("Write failed '%s'\n", err)
					}
				case smsg.WebAgentClose:
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
		payload := webdial.WebDataInActionPayload{
			RequestId:      s.requestId,
			SequenceNumber: s.sequenceNumber,
			Data:           dataToSend,
		}

		// Send payload to plugin output queue
		payloadBytes, _ := json.Marshal(payload)
		s.outputChan <- plugin.ActionWrapper{
			Action:        dialDataIn,
			ActionPayload: payloadBytes,
		}

		s.sequenceNumber += 1
	}

	return nil
}

func (s *WebAction) ReceiveMrZAP(wrappedAction plugin.ActionWrapper) {
	s.mzInputChan <- wrappedAction
}

func (s *WebAction) ReceiveStream(smessage smsg.StreamMessage) {
	s.logger.Debugf("Stream action received %v stream", smessage.Type)
	s.streamInputChan <- smessage
}
