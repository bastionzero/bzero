package webwebsocket

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/web/actions/webwebsocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/utils"
	"github.com/gorilla/websocket"

	"gopkg.in/tomb.v2"
)

const (
	startWebsocket  = "web/websocket/start"
	dataInWebsocket = "web/websocket/datain"
)

type WebWebsocketAction struct {
	logger *logger.Logger

	requestId string

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage
	ksInputChan     chan plugin.ActionWrapper

	sequenceNumber int
}

func New(logger *logger.Logger,
	requestId string) (*WebWebsocketAction, chan plugin.ActionWrapper) {

	stream := &WebWebsocketAction{
		logger: logger,

		requestId: requestId,

		outputChan:      make(chan plugin.ActionWrapper, 10),
		streamInputChan: make(chan smsg.StreamMessage, 10),
		ksInputChan:     make(chan plugin.ActionWrapper, 10),

		sequenceNumber: 0,
	}

	return stream, stream.outputChan
}

func (s *WebWebsocketAction) Start(tmb *tomb.Tomb, Writer http.ResponseWriter, Request *http.Request) error {
	// this action ends at the end of this function, in order to signal that to the parent plugin,
	// we close the output channel which will close the go routine listening on it
	defer close(s.outputChan)

	// First extract the headers out of the request
	headers := utils.GetHeaders(Request.Header)

	// Let the agent know to open up a websocket
	payload := webwebsocket.WebWebsocketStartActionPayload{
		RequestId: s.requestId,
		Headers:   headers,
		Endpoint:  Request.URL.String(),
	}

	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	s.outputChan <- plugin.ActionWrapper{
		Action:        startWebsocket,
		ActionPayload: payloadBytes,
	}

	return s.handleWebsocketRequest(Writer, Request)
}

func (s *WebWebsocketAction) handleWebsocketRequest(Writer http.ResponseWriter, Request *http.Request) error {
	// Upgrade the connection
	var upgrader = websocket.Upgrader{}
	conn, err := upgrader.Upgrade(Writer, Request, nil)
	if err != nil {
		log.Print("upgrade failed: ", err)
		return err
	}
	defer conn.Close()

	// Continuosly read and write message
	for {
		mt, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("read failed:", err)
			break
		}

		// Convert the message to a string
		messageBase64 := base64.StdEncoding.EncodeToString(message)

		// Send the input along with mt to our agent
		payload := webwebsocket.WebWebsocketDataInActionPayload{
			Message:     messageBase64,
			MessageType: mt,
		}

		// Send payload to plugin output queue
		payloadBytes, _ := json.Marshal(payload)
		s.outputChan <- plugin.ActionWrapper{
			Action:        dataInWebsocket,
			ActionPayload: payloadBytes,
		}

		// cmd := getCmd(input)
		// msg := getMessage(input)
		// if cmd == "add" {
		// 	todoList = append(todoList, msg)
		// } else if cmd == "done" {
		// 	updateTodoList(msg)
		// }
		// output := "Current Todos: \n"
		// for _, todo := range todoList {
		// 	output += "\n - " + todo + "\n"
		// }
		// output += "\n----------------------------------------"
		message = []byte("poopie")
		err = conn.WriteMessage(mt, message)
		// if err != nil {
		// 	log.Println("write failed:", err)
		// 	break
		// }
	}
	return nil
}

func (s *WebWebsocketAction) ReceiveKeysplitting(wrappedAction plugin.ActionWrapper) {
	s.ksInputChan <- wrappedAction
}

func (s *WebWebsocketAction) ReceiveStream(smessage smsg.StreamMessage) {
	s.logger.Debugf("Stream action received %v stream", smessage.Type)
	s.streamInputChan <- smessage
}
