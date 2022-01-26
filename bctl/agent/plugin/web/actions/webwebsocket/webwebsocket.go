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
	WebWebsocketStart   WebWebsocketSubAction = "web/websocket/start"
	WebWebsocketDataIn  WebWebsocketSubAction = "web/websocket/datain"
	WebWebsocketDataOut WebWebsocketSubAction = "web/websocket/dataout"
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

		// First validate the requestId
		if err := e.validateRequestId(webWebsocketDataIn.RequestId); err != nil {
			return "", []byte{}, err
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

	// Write the message to the websocket
	wsWriteError := e.ws.WriteMessage(webWebsocketDataIn.MessageType, messageDecoded)
	if wsWriteError != nil {
		return "", []byte{}, wsWriteError
	}

	return action, []byte{}, nil
}

func (e *WebWebsocket) StartWebsocket(webWebsocketStartRequest WebWebsocketStartActionPayload, action string) (string, []byte, error) {
	// Set our requestId
	e.requestId = webWebsocketStartRequest.RequestId

	// Open up our websocket
	// Ref: https://stackoverflow.com/questions/32745716/i-need-to-connect-to-an-existing-websocket-server-using-go-lang
	baseAddress := e.remoteHost + ":" + fmt.Sprint(e.remotePort)

	u := url.URL{Scheme: "ws", Host: baseAddress, Path: webWebsocketStartRequest.Endpoint}
	e.logger.Infof("Connecting to %s", u.String())

	ws, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		e.logger.Errorf("dial error: %s", err)
		return "", []byte{}, err
	}

	// Keep reading messages in a go function in the background
	go func() {
		for {
			mt, message, err := ws.ReadMessage()
			if err != nil {
				e.logger.Infof("Read websocket error: %s", err)
				return
			}

			// Forward this message along to the daemon
			toSend := WebWebsocketStreamDataOut{
				Message:     base64.StdEncoding.EncodeToString(message),
				MessageType: mt,
			}
			toSendBytes, err := json.Marshal(toSend)
			if err != nil {
				e.logger.Infof("Json marshell error: %s", err)
				return
			}
			content := base64.StdEncoding.EncodeToString(toSendBytes)

			// Stream the response back
			// TODO: Eventually we want a new type of stream message
			streamMessage := smsg.StreamMessage{
				Type:           string(WebWebsocketDataOut),
				RequestId:      e.requestId,
				LogId:          "", // No log id for web websocket
				SequenceNumber: 0,
				Content:        content,
			}
			e.streamOutputChan <- streamMessage

			e.logger.Infof("Recivied websocket message: %s", message)
		}
	}()

	e.ws = ws

	return action, []byte{}, nil
}

func (e *WebWebsocket) validateRequestId(requestId string) error {
	if requestId != e.requestId {
		rerr := fmt.Errorf("invalid request ID passed")
		e.logger.Error(rerr)
		return rerr
	}
	return nil
}
