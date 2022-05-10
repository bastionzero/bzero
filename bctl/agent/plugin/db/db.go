package db

import (
	"encoding/json"
	"fmt"
	"strings"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/db/actions/dial"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/db"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type IDbAction interface {
	Receive(action string, actionPayload []byte) ([]byte, error)
	Kill()
}

type DbPlugin struct {
	logger *logger.Logger

	action           IDbAction
	streamOutputChan chan smsg.StreamMessage
	doneChan         chan struct{}

	// Either use the host:port
	remotePort int
	remoteHost string
}

func New(logger *logger.Logger,
	ch chan smsg.StreamMessage,
	action string,
	payload []byte,
) (*DbPlugin, error) {

	// Unmarshal the Syn payload
	var syn db.DbActionParams
	if err := json.Unmarshal(payload, &syn); err != nil {
		return nil, fmt.Errorf("malformed Db plugin SYN payload %v", string(payload))
	}

	// Create our plugin
	plugin := &DbPlugin{
		logger:           logger,
		streamOutputChan: ch,
		doneChan:         make(chan struct{}),
		remotePort:       syn.RemotePort,
		remoteHost:       syn.RemoteHost,
	}

	// Start up the action for this plugin
	subLogger := plugin.logger.GetActionLogger(action)
	if parsedAction, err := parseAction(action); err != nil {
		return nil, err
	} else {
		var rerr error

		switch parsedAction {
		case db.Dial:
			plugin.action, rerr = dial.New(subLogger, plugin.streamOutputChan, plugin.doneChan, syn.RemoteHost, syn.RemotePort)
		default:
			rerr = fmt.Errorf("unhandled DB action")
		}

		if rerr != nil {
			return nil, fmt.Errorf("failed to start DB plugin with action %s: %s", action, rerr)
		} else {
			plugin.logger.Infof("DB plugin started with %v action", action)
			return plugin, nil
		}
	}
}

func (d *DbPlugin) Done() <-chan struct{} {
	return d.doneChan
}

func (d *DbPlugin) Kill() {
	if d.action != nil {
		d.action.Kill()
	}
}

func (d *DbPlugin) Receive(action string, actionPayload []byte) ([]byte, error) {
	d.logger.Debugf("DB plugin received message with %s action", action)

	if payload, err := d.action.Receive(action, actionPayload); err != nil {
		return []byte{}, err
	} else {
		return payload, err
	}
}

func parseAction(action string) (db.DbAction, error) {
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", fmt.Errorf("malformed action: %s", action)
	}
	return db.DbAction(parsedAction[1]), nil
}
