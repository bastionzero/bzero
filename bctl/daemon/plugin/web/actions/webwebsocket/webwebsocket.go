package webwebsocket

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/web/actions/webwebsocket"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"github.com/gorilla/websocket"

	"gopkg.in/tomb.v2"
)

type WebWebsocketAction struct {
	logger *logger.Logger

	requestId string

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage

	sequenceNumber int
}

func New(logger *logger.Logger,
	requestId string) (*WebWebsocketAction, chan plugin.ActionWrapper) {

	stream := &WebWebsocketAction{
		logger: logger,

		requestId: requestId,

		outputChan:      make(chan plugin.ActionWrapper, 10),
		streamInputChan: make(chan smsg.StreamMessage, 10),

		sequenceNumber: 0,
	}

	return stream, stream.outputChan
}

func (s *WebWebsocketAction) Start(tmb *tomb.Tomb, writer http.ResponseWriter, request *http.Request) error {
	// this action ends at the end of this function, in order to signal that to the parent plugin,
	// we close the output channel which will close the go routine listening on it
	defer close(s.outputChan)

	// First extract the headers out of the request
	headers := bzhttp.GetHeaders(request.Header)

	// Let the agent know to open up a websocket
	payload := webwebsocket.WebWebsocketStartActionPayload{
		RequestId:            s.requestId,
		StreamMessageVersion: smsg.CurrentSchema,
		Headers:              headers,
		Endpoint:             request.URL.String(),
		Method:               request.Method,
	}

	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	s.outputChan <- plugin.ActionWrapper{
		Action:        string(webwebsocket.Start),
		ActionPayload: payloadBytes,
	}

	return s.handleWebsocketRequest(writer, request)
}

func (s *WebWebsocketAction) handleWebsocketRequest(writer http.ResponseWriter, request *http.Request) error {
	// Upgrade the connection
	var upgrader = websocket.Upgrader{}
	conn, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		log.Print("upgrade failed: ", err)
		return err
	}
	defer conn.Close()

	// Setup a go routine to stream data from the agent back to daemon
	go func() {
		for {
			incomingMessage := <-s.streamInputChan
			// may be getting an old-fashioned or newfangled message, depending on what we asked for
			if incomingMessage.Type == smsg.DataOut || incomingMessage.Type == smsg.Data {
				if writeErr := s.writeOutData(conn, incomingMessage.Content); writeErr != nil {
					return
				}
			}
			if incomingMessage.Type == smsg.AgentStop || incomingMessage.Type == smsg.Stop {
				// End the local connection
				s.logger.Infof("Received close message from agent, closing websocket")
				conn.Close()
				return
			} else {
				s.logger.Errorf("unhandled stream type: %s", incomingMessage.Type)
			}
		}
	}()

	// Continuosly read
	for {
		mt, message, err := conn.ReadMessage()
		if err != nil {
			s.logger.Infof("Read failed: %s", err)

			// Build our stop message
			payload := webwebsocket.WebWebsocketDaemonStopActionPayload{
				RequestId: s.requestId,
			}

			// Send payload to plugin output queue
			payloadBytes, _ := json.Marshal(payload)

			// Let the agent know to close the websocket
			s.outputChan <- plugin.ActionWrapper{
				Action:        string(webwebsocket.DaemonStop),
				ActionPayload: payloadBytes,
			}
			break
		}

		// Convert the message to a string
		messageBase64 := base64.StdEncoding.EncodeToString(message)

		// Send the input along with mt to our agent
		payload := webwebsocket.WebWebsocketDataInActionPayload{
			RequestId:   s.requestId,
			Message:     messageBase64,
			MessageType: mt,
		}

		// Send payload to plugin output queue
		payloadBytes, _ := json.Marshal(payload)
		s.outputChan <- plugin.ActionWrapper{
			Action:        string(webwebsocket.DataIn),
			ActionPayload: payloadBytes,
		}
	}
	return nil
}

func (s *WebWebsocketAction) ReceiveKeysplitting(wrappedAction plugin.ActionWrapper) {
	// We do not need to receive any keysplitting messages
}

func (s *WebWebsocketAction) ReceiveStream(smessage smsg.StreamMessage) {
	s.logger.Debugf("web websocket action received %v stream", smessage.Type)
	s.streamInputChan <- smessage
}

func (s *WebWebsocketAction) writeOutData(conn *websocket.Conn, content string) error {
	// Stream data to the local connection
	// Undo the base 64 encoding
	incomingContent, base64Err := base64.StdEncoding.DecodeString(content)
	if base64Err != nil {
		s.logger.Errorf("error decoding stream message: %v", base64Err)
		return base64Err
	}

	// Unmarshell the stream message
	var streamDataOut webwebsocket.WebWebsocketStreamDataOut
	if err := json.Unmarshal(incomingContent, &streamDataOut); err != nil {
		s.logger.Errorf("error unmarshalling stream message: %s", err)
		return err
	}

	// Unmarshel the websocket message
	websocketMessage, base64Err := base64.StdEncoding.DecodeString(streamDataOut.Message)
	if base64Err != nil {
		s.logger.Errorf("error decoding stream message: %s", base64Err)
		return base64Err
	}

	// Send the message to the user!
	err := conn.WriteMessage(streamDataOut.MessageType, websocketMessage)
	if err != nil {
		s.logger.Errorf("error writing to websocket: %s", err)
	}
	return nil
}
