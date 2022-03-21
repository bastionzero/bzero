package db

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/db/actions/dial"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/db"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

type IDbAction interface {
	Receive(action string, actionPayload []byte) (string, []byte, error)
	Closed() bool
}

type DbPlugin struct {
	logger *logger.Logger
	tmb    *tomb.Tomb // datachannel's tomb

	streamOutputChan chan smsg.StreamMessage

	// Either use the host:port
	remotePort int
	remoteHost string

	remoteAddress *net.TCPAddr

	// Keep track of all the dials TCP connections
	action IDbAction
}

func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	payload []byte) (*DbPlugin, error) {

	// Unmarshal the Syn payload
	var synPayload db.DbActionParams
	if err := json.Unmarshal(payload, &synPayload); err != nil {
		return nil, fmt.Errorf("malformed Db plugin SYN payload %v", string(payload))
	}

	// Build our address
	address := fmt.Sprintf("%s:%v", synPayload.RemoteHost, synPayload.RemotePort)

	// Open up a connection to the TCP addr we are trying to connect to
	raddr, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		logger.Errorf("Failed to resolve remote address: %s", err)
		return nil, fmt.Errorf("failed to resolve remote address: %s", err)
	}

	plugin := &DbPlugin{
		remotePort:       synPayload.RemotePort,
		remoteHost:       synPayload.RemoteHost,
		logger:           logger,
		tmb:              parentTmb, // if datachannel dies, so should we
		streamOutputChan: ch,
		remoteAddress:    raddr,
	}

	return plugin, nil
}

func (d *DbPlugin) Receive(action string, actionPayload []byte) (string, []byte, error) {
	d.logger.Infof("DB plugin received Data message with %v action", action)

	var rerr error
	if parsedAction, err := parseAction(action); err != nil {
		rerr = err
	} else if safePayload, err := cleanPayload(actionPayload); err != nil {
		rerr = err
	} else {

		// if we don't have an action for this plugin, start it
		if d.action == nil {
			subLogger := d.logger.GetActionLogger(action)

			switch parsedAction {
			case db.Dial:
				if act, err := dial.New(subLogger, d.tmb, d.streamOutputChan, d.remoteAddress); err != nil {
					rerr = fmt.Errorf("could not start new action object: %s", err)
				} else {
					d.action = act
				}
			default:
				rerr = fmt.Errorf("unhandled db action: %v", action)
			}
		}

		// only continue if we didn't hit an error in the previous section
		if rerr == nil {
			if action, payload, err := d.action.Receive(action, safePayload); err != nil {
				rerr = err
			} else {
				return action, payload, err
			}
		}
	}

	// if we're here, we hit an error
	d.logger.Error(rerr)
	return "", []byte{}, rerr
}

func parseAction(action string) (db.DbAction, error) {
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", fmt.Errorf("malformed action: %s", action)
	}
	return db.DbAction(parsedAction[1]), nil
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
