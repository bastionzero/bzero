package db

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/db/actions/dial"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/db"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

type JustRequestId struct {
	RequestId string `json:"requestId"`
}

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
	actions        map[string]IDbAction
	actionsMapLock sync.Mutex
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
		actions:          make(map[string]IDbAction),
	}

	return plugin, nil
}

func (d *DbPlugin) Receive(action string, actionPayload []byte) (string, []byte, error) {
	d.logger.Infof("Plugin received Data message with %v action", action)

	// parse action
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", []byte{}, fmt.Errorf("malformed action: %s", action)
	}
	dbAction := parsedAction[1]

	// TODO: The below line removes the extra, surrounding quotation marks that get added at some point in the marshal/unmarshal
	// so it messes up the umarshalling into a valid action payload.  We need to figure out why this is happening
	// so that we can murder its family
	if len(actionPayload) > 0 {
		actionPayload = actionPayload[1 : len(actionPayload)-1]
	}

	// Json unmarshalling encodes bytes in base64
	actionPayloadSafe, base64Err := base64.StdEncoding.DecodeString(string(actionPayload))
	if base64Err != nil {
		d.logger.Errorf("error decoding actionPayload: %v", base64Err)
		return "", []byte{}, base64Err
	}

	// Grab just the request ID so that we can look up whether it's associated with a previously started action object
	var justrid JustRequestId
	var rid string
	if err := json.Unmarshal(actionPayloadSafe, &justrid); err != nil {
		return "", []byte{}, fmt.Errorf("could not unmarshal json: %v", err.Error())
	} else {
		rid = justrid.RequestId
	}

	// Lookup if we already started an action for this requestId
	if act, ok := d.getActionsMap(rid); ok {
		// If we did, send the payload data to this action
		action, payload, err := act.Receive(action, actionPayloadSafe)

		// Check if that last message closed the action, if so delete from map
		if act.Closed() {
			d.deleteActionsMap(rid)
		}

		return action, payload, err
	} else {
		subLogger := d.logger.GetActionLogger(action)
		subLogger.AddRequestId(rid)
		switch db.DbAction(dbAction) {
		case db.Dial:
			// Create a new dbdial action
			a, err := dial.New(subLogger, d.tmb, d.streamOutputChan, d.remoteAddress)

			if err != nil {
				rerr := fmt.Errorf("could not start new action object: %s", err)
				d.logger.Error(rerr)
				return "", []byte{}, rerr
			}

			// Send the payload to the action and add it to the map for future incoming requests
			action, payload, err := a.Receive(action, actionPayloadSafe)

			// The start dial action can cause the action to close initally
			// (i.e. if there is nothing on the other end and we are unable to resolve that tcp addr)
			if !a.Closed() {
				d.updateActionsMap(a, rid) // save action for later input
			}

			return action, payload, err
		default:
			rerr := fmt.Errorf("unhandled db action: %v", action)
			d.logger.Error(rerr)
			return "", []byte{}, rerr
		}
	}
}

// Helper function so we avoid writing to this map at the same time
func (k *DbPlugin) updateActionsMap(newAction IDbAction, id string) {
	k.actionsMapLock.Lock()
	k.actions[id] = newAction
	k.actionsMapLock.Unlock()
}

func (k *DbPlugin) deleteActionsMap(rid string) {
	k.actionsMapLock.Lock()
	delete(k.actions, rid)
	k.actionsMapLock.Unlock()
}

func (k *DbPlugin) getActionsMap(rid string) (IDbAction, bool) {
	k.actionsMapLock.Lock()
	defer k.actionsMapLock.Unlock()
	act, ok := k.actions[rid]
	return act, ok
}
