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

	action           IWebAction
	streamOutputChan chan smsg.StreamMessage

	// remote host:port
	remotePort int
	remoteHost string
}

func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	action string,
	payload []byte) (*WebPlugin, error) {

	// Unmarshal the Syn payload
	var actionPayload bzweb.WebActionParams
	if err := json.Unmarshal(payload, &actionPayload); err != nil {
		return nil, fmt.Errorf("malformed web plugin SYN payload %s", string(payload))
	}

	plugin := &WebPlugin{
		tmb:    parentTmb, // if datachannel dies, so should we
		logger: logger,

		streamOutputChan: ch,

		remotePort: actionPayload.RemotePort,
		remoteHost: actionPayload.RemoteHost,
	}

	// start the action for the plugin
	subLogger := plugin.logger.GetActionLogger(action)

	var rerr error
	if parsedAction, err := parseAction(action); err != nil {
		rerr = err
	} else {
		switch parsedAction {
		case bzweb.Dial:
			plugin.action, rerr = webdial.New(subLogger, plugin.remoteHost, plugin.remotePort, plugin.tmb, plugin.streamOutputChan)
		case bzweb.Websocket:
			plugin.action, rerr = webwebsocket.New(subLogger, plugin.remoteHost, plugin.remotePort, plugin.tmb, plugin.streamOutputChan)
		default:
			rerr = fmt.Errorf("unhandled web action")
		}
	}

	if rerr != nil {
		plugin.logger.Errorf("failed to start plugin action %s: %s", action, rerr)
		return nil, rerr
	} else {
		plugin.logger.Infof("Web plugin started %v action", action)
		return plugin, nil
	}
}

func (w *WebPlugin) Receive(action string, actionPayload []byte) (string, []byte, error) {
	w.logger.Debugf("Web plugin received message with %v action", action)

	var rerr error
	if safePayload, err := cleanPayload(actionPayload); err != nil {
		rerr = err
	} else if action, payload, err := w.action.Receive(action, safePayload); err != nil {
		rerr = err
	} else {
		return action, payload, err
	}

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
