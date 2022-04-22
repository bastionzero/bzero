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
	Receive(action string, actionPayload []byte) (string, []byte, error)
	Done() <-chan struct{}
	Stop()
}

type DbPlugin struct {
	logger *logger.Logger

	action           IDbAction
	streamOutputChan chan smsg.StreamMessage

	// Either use the host:port
	remotePort int
	remoteHost string
}

func New(logger *logger.Logger,
	ch chan smsg.StreamMessage,
	action string,
	payload []byte) (*DbPlugin, error) {

	// Unmarshal the Syn payload
	var synPayload db.DbActionParams
	if err := json.Unmarshal(payload, &synPayload); err != nil {
		return nil, fmt.Errorf("malformed Db plugin SYN payload %v", string(payload))
	}

	// Create our plugin
	plugin := &DbPlugin{
		logger:           logger,
		streamOutputChan: ch,
		remotePort:       synPayload.RemotePort,
		remoteHost:       synPayload.RemoteHost,
	}

	// Start up the action for this plugin
	subLogger := plugin.logger.GetActionLogger(action)
	if parsedAction, err := parseAction(action); err != nil {
		return nil, err
	} else {
		var rerr error

		switch parsedAction {
		case db.Dial:
			plugin.action, rerr = dial.New(subLogger, plugin.streamOutputChan, synPayload.RemoteHost, synPayload.RemotePort)
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
	if d.action != nil {
		return d.action.Done()
	} else {
		doneChan := make(chan struct{})
		close(doneChan)
		return doneChan
	}
}

func (d *DbPlugin) Stop() {
	if d.action != nil {
		d.action.Stop()
	}
}

func (d *DbPlugin) Receive(action string, actionPayload []byte) (string, []byte, error) {
	d.logger.Debugf("DB plugin received message with %s action", action)

	// if safePayload, err := cleanPayload(actionPayload); err != nil {
	// 	d.logger.Error(err)
	// 	return "", []byte{}, err
	// } else
	if action, payload, err := d.action.Receive(action, actionPayload); err != nil {
		return "", []byte{}, err
	} else {
		return action, payload, err
	}

	// d.logger.Error(rerr)
	// return "", []byte{}, rerr
}

func parseAction(action string) (db.DbAction, error) {
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", fmt.Errorf("malformed action: %s", action)
	}
	return db.DbAction(parsedAction[1]), nil
}

// func cleanPayload(payload []byte) ([]byte, error) {
// 	// TODO: The below line removes the extra, surrounding quotation marks that get added at some point in the marshal/unmarshal
// 	// so it messes up the umarshalling into a valid action payload.  We need to figure out why this is happening
// 	// so that we can murder its family
// 	if len(payload) > 0 {
// 		payload = payload[1 : len(payload)-1]
// 	}

// 	// Json unmarshalling encodes bytes in base64
// 	if payloadSafe, err := base64.StdEncoding.DecodeString(string(payload)); err != nil {
// 		return []byte{}, fmt.Errorf("error decoding actionPayload: %s", err)
// 	} else {
// 		return payloadSafe, nil
// 	}
// }
