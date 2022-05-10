package web

import (
	"encoding/json"
	"fmt"
	"strings"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/web/actions/webdial"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/web/actions/webwebsocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzweb "bastionzero.com/bctl/v1/bzerolib/plugin/web"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type IWebAction interface {
	Receive(action string, actionPayload []byte) ([]byte, error)
	Kill()
}

type WebPlugin struct {
	logger *logger.Logger

	action           IWebAction
	streamOutputChan chan smsg.StreamMessage
	doneChan         chan struct{}

	// remote host:port
	remotePort int
	remoteHost string
}

func New(
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	action string,
	payload []byte,
) (*WebPlugin, error) {

	// Unmarshal the Syn payload
	var actionPayload bzweb.WebActionParams
	if err := json.Unmarshal(payload, &actionPayload); err != nil {
		return nil, fmt.Errorf("malformed web plugin SYN payload")
	}

	plugin := &WebPlugin{
		logger:           logger,
		streamOutputChan: ch,
		doneChan:         make(chan struct{}),
		remotePort:       actionPayload.RemotePort,
		remoteHost:       actionPayload.RemoteHost,
	}

	// start the action for the plugin
	subLogger := plugin.logger.GetActionLogger(action)

	var rerr error
	if parsedAction, err := parseAction(action); err != nil {
		rerr = err
	} else {
		switch parsedAction {
		case bzweb.Dial:
			plugin.action, rerr = webdial.New(subLogger, plugin.streamOutputChan, plugin.doneChan, plugin.remoteHost, plugin.remotePort)
		case bzweb.Websocket:
			plugin.action, rerr = webwebsocket.New(subLogger, plugin.streamOutputChan, plugin.doneChan, plugin.remoteHost, plugin.remotePort)
		default:
			rerr = fmt.Errorf("unhandled Web action")
		}
	}

	if rerr != nil {
		return nil, rerr
	} else {
		plugin.logger.Infof("Web plugin started with %v action", action)
		return plugin, nil
	}
}

func (w *WebPlugin) Done() <-chan struct{} {
	return w.doneChan
}

func (w *WebPlugin) Kill() {
	if w.action != nil {
		w.action.Kill()
	}
}

func (w *WebPlugin) Receive(action string, actionPayload []byte) ([]byte, error) {
	w.logger.Debugf("Web plugin received message with %v action", action)

	if payload, err := w.action.Receive(action, actionPayload); err != nil {
		return []byte{}, err
	} else {
		return payload, err
	}
}

func parseAction(action string) (bzweb.WebAction, error) {
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", fmt.Errorf("malformed action: %s", action)
	}
	return bzweb.WebAction(parsedAction[1]), nil
}
