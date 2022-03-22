package web

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/web/actions/webdial"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/web/actions/webwebsocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzweb "bastionzero.com/bctl/v1/bzerolib/plugin/web"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

type IWebAction interface {
	Receive(action string, actionPayload []byte) (string, []byte, error)
	Closed() bool
}

type WebPlugin struct {
	tmb    *tomb.Tomb // datachannel's tomb
	logger *logger.Logger

	streamOutputChan chan smsg.StreamMessage

	// remote host:port
	remotePort int
	remoteHost string

	// Keep track of all the dials TCP connections
	action IWebAction
}

func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	payload []byte) (*WebPlugin, error) {

	// Unmarshal the Syn payload
	var actionPayload bzweb.WebActionParams
	if err := json.Unmarshal(payload, &actionPayload); err != nil {
		return nil, fmt.Errorf("malformed web plugin SYN payload %v", string(payload))
	}

	plugin := &WebPlugin{
		tmb:    parentTmb, // if datachannel dies, so should we
		logger: logger,

		streamOutputChan: ch,

		remotePort: actionPayload.RemotePort,
		remoteHost: actionPayload.RemoteHost,
	}

	return plugin, nil
}

func (w *WebPlugin) Receive(action string, actionPayload []byte) (string, []byte, error) {
	w.logger.Infof("Web plugin received Data message with %v action", action)

	var rerr error
	if parsedAction, err := parseAction(action); err != nil {
		rerr = err
	} else if safePayload, err := cleanPayload(actionPayload); err != nil {
		rerr = err
	} else {

		// if we don't have an action for this plugin, start it
		if w.action == nil {
			subLogger := w.logger.GetActionLogger(action)

			switch parsedAction {
			case bzweb.Dial:
				// Create a new web dial action
				w.action, rerr = webdial.New(subLogger, w.remoteHost, w.remotePort, w.tmb, w.streamOutputChan)
			case bzweb.Websocket:
				// Create a new web websocket action
				w.action, rerr = webwebsocket.New(subLogger, w.remoteHost, w.remotePort, w.tmb, w.streamOutputChan)
			default:
				rerr = fmt.Errorf("unhandled db action: %v", action)
			}
		}

		// only continue if we didn't hit an error in the previous section
		if rerr == nil {
			if action, payload, err := w.action.Receive(action, safePayload); err != nil {
				rerr = err
			} else {
				return action, payload, err
			}
		}
	}

	// if we're here, we hit an error
	w.logger.Error(rerr)
	return "", []byte{}, rerr
}

func parseAction(action string) (bzweb.WebAction, error) {
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", fmt.Errorf("malformed action: %s", action)
	}
	return bzweb.WebAction(parsedAction[1]), nil
}

func cleanPayload(payload []byte) ([]byte, error) {
	// TODO: The below line removes the extra, surrounding quotation marks that get added at some point in the marshal/unmarshal
	// so it messes up the umarshalling into a valid action payload.  We need to figure out why this is happening
	// so that we can murder its family
	if len(payload) > 0 {
		payload = payload[1 : len(payload)-1]
	}

	// Json unmarshalling encodes bytes in base64
	if payloadSafe, err := base64.StdEncoding.DecodeString(string(payload)); err != nil {
		return []byte{}, fmt.Errorf("error decoding actionPayload: %s", err)
	} else {
		return payloadSafe, nil
	}
}
