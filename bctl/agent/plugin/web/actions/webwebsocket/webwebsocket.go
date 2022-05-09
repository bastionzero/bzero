package webwebsocket

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"gopkg.in/tomb.v2"

	webaction "bastionzero.com/bctl/v1/bzerolib/plugin/web"
	webwebsocket "bastionzero.com/bctl/v1/bzerolib/plugin/web/actions/webwebsocket"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"github.com/gorilla/websocket"
)

type WebWebsocket struct {
	tmb      tomb.Tomb
	logger   *logger.Logger
	doneChan chan struct{}

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	ws *websocket.Conn

	remoteHost string
	remotePort int
	requestId  string
}

func New(logger *logger.Logger,
	streamChan chan smsg.StreamMessage,
	doneChan chan struct{},
	remoteHost string,
	remotePort int) (*WebWebsocket, error) {

	return &WebWebsocket{
		logger:           logger,
		doneChan:         doneChan,
		streamOutputChan: streamChan,
		remoteHost:       remoteHost,
		remotePort:       remotePort,
	}, nil
}

func (w *WebWebsocket) Done() <-chan struct{} {
	return w.doneChan
}

func (w *WebWebsocket) Kill() {
	w.tmb.Killf("we've been told to stop")
	w.tmb.Wait()
}

func (w *WebWebsocket) Receive(action string, actionPayload []byte) ([]byte, error) {
	switch webwebsocket.WebWebsocketSubAction(action) {
	case webwebsocket.Start:
		// Deserialize the action payload, the only action passed is DataIn
		var webWebsocketStartRequest webwebsocket.WebWebsocketStartActionPayload
		if err := json.Unmarshal(actionPayload, &webWebsocketStartRequest); err != nil {
			rerr := fmt.Errorf("unable to unmarshal dataIn message: %s", err)
			w.logger.Error(rerr)
			return []byte{}, rerr
		}

		return w.startWebsocket(webWebsocketStartRequest, action)
	case webwebsocket.DataIn:
		// Deserialize the action payload, the only action passed is DataIn
		var webWebsocketDataIn webwebsocket.WebWebsocketDataInActionPayload
		if err := json.Unmarshal(actionPayload, &webWebsocketDataIn); err != nil {
			rerr := fmt.Errorf("unable to unmarshal dataIn message: %s", err)
			w.logger.Error(rerr)
			return []byte{}, rerr
		}

		return w.dataInWebsocket(webWebsocketDataIn, action)
	case webwebsocket.DaemonStop:
		// The daemon has closed the websocket, close this one as well
		// Deserialize the action payload, the only action passed is DataIn
		var webWebsocketDaemonStop webwebsocket.WebWebsocketDaemonStopActionPayload
		if err := json.Unmarshal(actionPayload, &webWebsocketDaemonStop); err != nil {
			rerr := fmt.Errorf("unable to unmarshal daemonStop message: %s", err)
			w.logger.Error(rerr)
			return []byte{}, rerr
		}

		if w.ws != nil {
			w.ws.Close()
		} else {
			w.logger.Info("Attempted to close websocket connection that does not exist")
		}

		return []byte{}, nil
	default:
		rerr := fmt.Errorf("unhandled stream action: %v", action)
		w.logger.Error(rerr)
		return []byte{}, rerr
	}
}

func (w *WebWebsocket) dataInWebsocket(webWebsocketDataIn webwebsocket.WebWebsocketDataInActionPayload, action string) ([]byte, error) {
	// Decode the message
	messageDecoded, err := base64.StdEncoding.DecodeString(webWebsocketDataIn.Message)
	if err != nil {
		return []byte{}, err
	}

	// Write the message to the websocket
	wsWriteError := w.ws.WriteMessage(webWebsocketDataIn.MessageType, messageDecoded)
	if wsWriteError != nil {
		return []byte{}, wsWriteError
	}

	return []byte{}, nil
}

func (w *WebWebsocket) startWebsocket(webWebsocketStartRequest webwebsocket.WebWebsocketStartActionPayload, action string) ([]byte, error) {
	// keep track of who we're talking to
	w.requestId = webWebsocketStartRequest.RequestId
	w.logger.Infof("Setting request id: %s", w.requestId)
	w.streamMessageVersion = webWebsocketStartRequest.StreamMessageVersion
	w.logger.Infof("Setting stream message version: %s", w.streamMessageVersion)

	// Remove the scheme from the remoteHost and determine the scheme
	scheme := "ws"
	baseAddress := fmt.Sprintf("%s:%v", w.remoteHost, w.remotePort)
	remoteHostUrl, parseErr := url.Parse(baseAddress)
	if parseErr != nil {
		w.logger.Errorf("error parsing remote host url: %s", parseErr)
		return []byte{}, parseErr
	}
	if remoteHostUrl.Scheme == "https" {
		scheme = "wss"
	}

	// Open up our websocket
	// Ref: https://stackoverflow.com/questions/32745716/i-need-to-connect-to-an-existing-websocket-server-using-go-lang

	u := url.URL{Scheme: scheme, Host: remoteHostUrl.Host, Path: webWebsocketStartRequest.Endpoint}
	w.logger.Infof("Connecting to %s", u.String())

	ws, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		w.logger.Errorf("dial error: %s", err)
		// Do not return an error incase the user wants to try again in making this connection, rather send a close message
		switch w.streamMessageVersion {
		// prior to 202204
		case "":
			w.sendStreamMessage(0, smsg.AgentStop, false, []byte{})
		default:
			w.sendStreamMessage(0, smsg.Stop, false, []byte{})
		}
		return []byte{}, nil
	}

	// set our class variable so we can close it later
	w.ws = ws

	// Keep reading messages in a go function in the background
	sequenceNumber := 0
	w.tmb.Go(func() error {
		defer close(w.doneChan)

		for {
			if mt, message, err := ws.ReadMessage(); !w.tmb.Alive() {
				return nil
			} else if err != nil {
				w.logger.Infof("Read websocket error: %s", err)
				// We have to let the daemon know the websocket has ended
				switch w.streamMessageVersion {
				// prior to 202204
				case "":
					w.sendStreamMessage(sequenceNumber, smsg.AgentStop, false, []byte{})
				default:
					w.sendStreamMessage(sequenceNumber, smsg.Stop, false, []byte{})
				}
				return nil
			} else {
				// Forward this message along to the daemon
				toSend := webwebsocket.WebWebsocketStreamDataOut{
					Message:     base64.StdEncoding.EncodeToString(message),
					MessageType: mt,
				}
				contentBytes, err := json.Marshal(toSend)
				if err != nil {
					rerr := fmt.Errorf("json marshall error: %s", err)
					w.logger.Error(rerr)
					return rerr
				}
				switch w.streamMessageVersion {
				// prior to 202204
				case "":
					w.sendStreamMessage(sequenceNumber, smsg.DataOut, true, contentBytes)
				default:
					w.sendStreamMessage(sequenceNumber, smsg.Data, true, contentBytes)
				}
				sequenceNumber += 1

				w.logger.Tracef("Received websocket message: %s", message)
			}
		}
	})

	return []byte{}, nil
}

func (w *WebWebsocket) sendStreamMessage(sequenceNumber int, streamType smsg.StreamType, more bool, contentBytes []byte) {
	w.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion:  w.streamMessageVersion,
		SequenceNumber: sequenceNumber,
		Action:         string(webaction.Websocket),
		Type:           streamType,
		More:           more,
		Content:        base64.StdEncoding.EncodeToString(contentBytes),
	}
}
