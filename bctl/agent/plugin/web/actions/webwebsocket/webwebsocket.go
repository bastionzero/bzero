package webwebsocket

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"gopkg.in/tomb.v2"

	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"github.com/gorilla/websocket"
)

type WebWebsocketSubAction string

const (
	WebWebsocketStart  WebWebsocketSubAction = "web/websocket/start"
	WebWebsocketDataIn WebWebsocketSubAction = "web/websocket/datain"
)

type WebWebsocket struct {
	logger *logger.Logger
	tmb    *tomb.Tomb
	closed bool

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan chan smsg.StreamMessage

	ws *websocket.Conn

	wsToSend chan []byte

	remoteHost string
	remotePort int

	requestId string
}

func New(logger *logger.Logger,
	remoteHost string,
	remotePort int,
	pluginTmb *tomb.Tomb,
	ch chan smsg.StreamMessage) (*WebWebsocket, error) {

	return &WebWebsocket{
		logger:           logger,
		tmb:              pluginTmb,
		closed:           false,
		streamOutputChan: ch,
		wsToSend:         make(chan []byte, 10),
		remoteHost:       remoteHost,
		remotePort:       remotePort,
	}, nil
}

func (s *WebWebsocket) Closed() bool {
	return s.closed
}

func (e *WebWebsocket) Receive(action string, actionPayload []byte) (string, []byte, error) {
	switch WebWebsocketSubAction(action) {
	case WebWebsocketStart:
		// Deserialize the action payload, the only action passed is DataIn
		var webWebsocketStartRequest WebWebsocketStartActionPayload
		if err := json.Unmarshal(actionPayload, &webWebsocketStartRequest); err != nil {
			rerr := fmt.Errorf("unable to unmarshal dataIn message: %s", err)
			e.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		return e.StartWebsocket(webWebsocketStartRequest, action)
	case WebWebsocketDataIn:
		// Deserialize the action payload, the only action passed is DataIn
		var webWebsocketDataIn WebWebsocketDataInActionPayload
		if err := json.Unmarshal(actionPayload, &webWebsocketDataIn); err != nil {
			rerr := fmt.Errorf("unable to unmarshal dataIn message: %s", err)
			e.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		return e.DataInWebsocket(webWebsocketDataIn, action)
	default:
		rerr := fmt.Errorf("unhandled stream action: %v", action)
		e.logger.Error(rerr)
		return "", []byte{}, rerr
	}
}

func (e *WebWebsocket) DataInWebsocket(webWebsocketDataIn WebWebsocketDataInActionPayload, action string) (string, []byte, error) {
	// Decode the message
	messageDecoded, err := base64.StdEncoding.DecodeString(webWebsocketDataIn.Message)
	if err != nil {
		return "", []byte{}, err
	}
	e.logger.Infof("HERE: %v", e.ws)
	e.wsToSend <- messageDecoded

	// // Write this data to our websocket
	// if _, err := e.ws.Write([]byte("hello, world!\n")); err != nil {
	// 	return "", []byte{}, err
	// }
	// websocket.Message.Send(e.ws, []byte("hello, world!\n"))

	return action, []byte{}, nil
}

func (e *WebWebsocket) StartWebsocket(webWebsocketStartRequest WebWebsocketStartActionPayload, action string) (string, []byte, error) {
	// Set our requestId
	e.requestId = webWebsocketStartRequest.RequestId

	// Open up our websocket
	// Ref: https://stackoverflow.com/questions/32745716/i-need-to-connect-to-an-existing-websocket-server-using-go-lang
	baseAddress := e.remoteHost + ":" + fmt.Sprint(e.remotePort)

	u := url.URL{Scheme: "ws", Host: baseAddress, Path: webWebsocketStartRequest.Endpoint}
	e.logger.Infof("connecting to %s", u.String())

	// ws, err := websocket.Dial(fmt.Sprintf("ws://%s/", address), "", fmt.Sprintf("http://%s/", address))
	// if err != nil {
	// 	return "", []byte{}, fmt.Errorf("dial failed: %s", err.Error())
	// }

	// Save our websocket object so as data comes in we can write to it
	e.ws = ws

	// Keep reading messages into a channel in the background
	incomingMessages := make(chan string)
	go readClientMessages(ws, incomingMessages)

	// Listen for messages in the background and forward to the client
	go func() {
		for {
			select {
			case message := <-incomingMessages:
				fmt.Println(`Message Received:`, message)
			}
		}
	}()

	// Listen for incoming messages from the client
	go func() {
		for {
			select {
			case message := <-e.wsToSend:
				e.logger.Infof("HERE: %v", message)
				if _, err := e.ws.Write([]byte("hello, world!\n")); err != nil {
					return
				}
			}
		}
	}()

	return action, []byte{}, nil
}

func readClientMessages(ws *websocket.Conn, incomingMessages chan string) {
	for {
		var message string
		err := websocket.Message.Receive(ws, &message)
		if err != nil {
			fmt.Printf("Error::: %s\n", err.Error())
			return
		}
		incomingMessages <- message
	}
}

func (e *WebWebsocket) validateRequestId(requestId string) error {
	if requestId != e.requestId {
		rerr := fmt.Errorf("invalid request ID passed")
		e.logger.Error(rerr)
		return rerr
	}
	return nil
}
