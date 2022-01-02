package web

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/web/actions/webdial"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

type WebAction string

const (
	Dial WebAction = "dial"
	// Start  DbAction = "start"
	// DataIn DbAction = "datain"
)

type WebActionParams struct {
	TargetPort     string
	TargetHost     string
	TargetHostName string
}

type JustRequestId struct {
	RequestId string `json:"requestId"`
}

type IWebAction interface {
	Receive(action string, actionPayload []byte) (string, []byte, error)
	Closed() bool
}

type WebPlugin struct {
	tmb *tomb.Tomb // datachannel's tomb

	logger *logger.Logger

	streamOutputChan chan smsg.StreamMessage

	// Either use the host:port
	targetPort string
	targetHost string

	// Or the full host name (i.e. DNS entry)
	targetHostName string

	remoteAddress *net.TCPAddr

	// Keep track of all the dials TCP connections
	actions        map[string]IWebAction
	actionsMapLock sync.Mutex
}

func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	payload []byte) (*WebPlugin, error) {

	// Unmarshal the Syn payload
	var synPayload WebActionParams
	if err := json.Unmarshal(payload, &synPayload); err != nil {
		return &WebPlugin{}, fmt.Errorf("malformed Db plugin SYN payload %v", string(payload))
	}

	// Determine if we are using target hostname or host:port
	address := synPayload.TargetHostName
	if address == "" {
		address = synPayload.TargetHost + ":" + synPayload.TargetPort
	}

	// Open up a connection to the TCP addr we are trying to connect to
	raddr, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		logger.Errorf("Failed to resolve remote address: %s", err)
		return &WebPlugin{}, fmt.Errorf("failed to resolve remote address: %s", err)
	}

	plugin := &WebPlugin{
		targetPort:       synPayload.TargetPort,
		targetHost:       synPayload.TargetHost,
		targetHostName:   synPayload.TargetHostName,
		logger:           logger,
		tmb:              parentTmb, // if datachannel dies, so should we
		streamOutputChan: ch,
		remoteAddress:    raddr,
		actions:          make(map[string]IWebAction),
	}

	return plugin, nil
}

func (k *WebPlugin) Receive(action string, actionPayload []byte) (string, []byte, error) {
	k.logger.Infof("Plugin received Data message with %v action", action)

	// parse action
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", []byte{}, fmt.Errorf("malformed action: %s", action)
	}
	webAction := parsedAction[1]

	// TODO: The below line removes the extra, surrounding quotation marks that get added at some point in the marshal/unmarshal
	// so it messes up the umarshalling into a valid action payload.  We need to figure out why this is happening
	// so that we can murder its family
	if len(actionPayload) > 0 {
		actionPayload = actionPayload[1 : len(actionPayload)-1]
	}

	// Json unmarshalling encodes bytes in base64
	actionPayloadSafe, base64Err := base64.StdEncoding.DecodeString(string(actionPayload))
	if base64Err != nil {
		k.logger.Errorf("error decoding actionPayload: %v", base64Err)
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
	if act, ok := k.getActionsMap(rid); ok {
		// If we did, send the payload data to this action
		action, payload, err := act.Receive(action, actionPayloadSafe)

		// Check if that last message closed the action, if so delete from map
		if act.Closed() {
			k.deleteActionsMap(rid)
		}

		return action, payload, err
	} else {
		subLogger := k.logger.GetActionLogger(action)
		subLogger.AddRequestId(rid)
		switch WebAction(webAction) {
		case Dial:
			// Create a new dbdial action
			a, err := webdial.New(subLogger, k.tmb, k.streamOutputChan, k.remoteAddress)
			k.updateActionsMap(a, rid) // save action for later input

			if err != nil {
				rerr := fmt.Errorf("could not start new action object: %s", err)
				k.logger.Error(rerr)
				return "", []byte{}, rerr
			}

			// Send the payload to the action and add it to the map for future incoming requests
			action, payload, err := a.Receive(action, actionPayloadSafe)
			return action, payload, err
		default:
			rerr := fmt.Errorf("unhandled db action: %v", action)
			k.logger.Error(rerr)
			return "", []byte{}, rerr
		}
	}

}

// Helper function so we avoid writing to this map at the same time
func (k *WebPlugin) updateActionsMap(newAction IWebAction, id string) {
	k.actionsMapLock.Lock()
	k.actions[id] = newAction
	k.actionsMapLock.Unlock()
}

func (k *WebPlugin) deleteActionsMap(rid string) {
	k.actionsMapLock.Lock()
	delete(k.actions, rid)
	k.actionsMapLock.Unlock()
}

func (k *WebPlugin) getActionsMap(rid string) (IWebAction, bool) {
	k.actionsMapLock.Lock()
	defer k.actionsMapLock.Unlock()
	act, ok := k.actions[rid]
	return act, ok
}