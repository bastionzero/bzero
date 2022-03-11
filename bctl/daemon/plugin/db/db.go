package db

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"

	"github.com/google/uuid"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/db/actions/dial"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzdb "bastionzero.com/bctl/v1/bzerolib/plugin/db"
	bzdial "bastionzero.com/bctl/v1/bzerolib/plugin/db/actions/dial"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// Perhaps unnecessary but it is nice to make sure that each action is implementing a common function set
type IDbDaemonAction interface {
	ReceiveKeysplitting(wrappedAction plugin.ActionWrapper)
	ReceiveStream(stream smsg.StreamMessage)
	Start(tmb *tomb.Tomb, lconn *net.TCPConn) error
}

type DbDaemonPlugin struct {
	tmb    *tomb.Tomb
	logger *logger.Logger

	// Input and output channels
	streamInputChan chan smsg.StreamMessage
	outputQueue     chan plugin.ActionWrapper

	actions       map[string]IDbDaemonAction
	actionMapLock sync.RWMutex // for keeping the action map thread-safe

	// Db-specific vars
	sequenceNumber int
}

func New(parentTmb *tomb.Tomb, logger *logger.Logger, actionParams bzdb.DbActionParams) (*DbDaemonPlugin, error) {
	plugin := DbDaemonPlugin{
		tmb:             parentTmb,
		logger:          logger,
		streamInputChan: make(chan smsg.StreamMessage, 25),
		outputQueue:     make(chan plugin.ActionWrapper, 100),
		actions:         make(map[string]IDbDaemonAction),
		sequenceNumber:  0,
	}

	// listener for processing any incoming stream messages, since they are not treated as part of
	// the keysplitting synchronous chain
	go func() {
		for {
			select {
			case <-plugin.tmb.Dying():
				return
			case streamMessage := <-plugin.streamInputChan:
				plugin.processStream(streamMessage)
			}
		}
	}()

	return &plugin, nil
}

func (d *DbDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	d.logger.Debugf("Stream action received %v stream", smessage.Type)
	d.streamInputChan <- smessage
}

func (d *DbDaemonPlugin) processStream(smessage smsg.StreamMessage) error {
	// find action by requestid in map and push stream message to it
	if act, ok := d.getActionsMap(smessage.RequestId); ok {
		act.ReceiveStream(smessage)
		return nil
	}

	rerr := fmt.Errorf("unknown request ID: %v. This is expected if the action has already been completed", smessage.RequestId)
	d.logger.Error(rerr)
	return rerr
}

func (d *DbDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) (string, []byte, error) {
	// First, process the incoming message
	if err := d.processKeysplitting(action, actionPayload); err != nil {
		return "", []byte{}, err
	}

	// Now that we've received, we wait for any new outgoing commands.  Because the existence of any
	// such command is dependent on the user, there may not be one waiting so we wait for it.
	d.logger.Info("Waiting for input...")

	select {
	case <-d.tmb.Dying():
		return "", []byte{}, nil
	case actionMessage := <-d.outputQueue: // some action's got something to say
		d.logger.Infof("Sending input from action: %v", actionMessage.Action)

		// turn the actionPayload into bytes and return it
		if actionPayloadBytes, err := json.Marshal(actionMessage.ActionPayload); err != nil {
			d.logger.Infof("actionPayload: %+v", actionPayload)
			return "", []byte{}, fmt.Errorf("could not marshal actionPayload json: %s", err)
		} else {
			return actionMessage.Action, actionPayloadBytes, nil
		}
	}
}

func (d *DbDaemonPlugin) processKeysplitting(action string, actionPayload []byte) error {

	// currently the only keysplitting message we care about is the acknowledgement of our request for the agent to stop the dial action
	if action == string(bzdial.DialStop) {
		var dbStop bzdial.DialActionPayload
		if err := json.Unmarshal(actionPayload, &dbStop); err != nil {
			return fmt.Errorf("could not unmarshal json: %s", err)
		} else {
			if act, ok := d.getActionsMap(dbStop.RequestId); ok {
				act.ReceiveKeysplitting(plugin.ActionWrapper{
					Action:        action,
					ActionPayload: actionPayload,
				})
				return nil
			}
		}
	}
	return nil
}

func (d *DbDaemonPlugin) Feed(food interface{}) error {
	// Make sure our food matches the nutrition label
	dbFood, ok := food.(bzdb.DbFood)
	if !ok {
		return fmt.Errorf("db food did not match nutrition label: %+v", food)
	}

	// Always generate a requestId, each new db command is its own request
	requestId := uuid.New().String()

	// Create action logger
	actLogger := d.logger.GetActionLogger(string(dbFood.Action))
	actLogger.AddRequestId(requestId)

	var act IDbDaemonAction
	var actOutputChan chan plugin.ActionWrapper

	switch bzdb.DbAction(dbFood.Action) {
	case bzdb.Dial:
		act, actOutputChan = dial.New(actLogger, requestId)
	default:
		rerr := fmt.Errorf("unrecognized db action: %v", string(dbFood.Action))
		d.logger.Error(rerr)
		return rerr
	}

	// add the action to the action map for future interaction
	d.updateActionsMap(act, requestId)

	// listen to action output channel, remove action from map if channel is closed
	go func() {
		for {
			select {
			case <-d.tmb.Dying():
				return
			case m, more := <-actOutputChan:
				if more {
					d.outputQueue <- m
				} else {
					d.deleteActionsMap(requestId)
					return
				}
			}
		}
	}()

	d.logger.Infof("Created %s action with requestId %v", string(dbFood.Action), requestId)

	// send local tcp connection to action
	if err := act.Start(d.tmb, dbFood.Conn); err != nil {
		d.logger.Error(fmt.Errorf("%s error: %s", string(dbFood.Action), err))
	}

	return nil
}

func (d *DbDaemonPlugin) updateActionsMap(newAction IDbDaemonAction, id string) {
	// Helper function so we avoid writing to this map at the same time
	d.actionMapLock.Lock()
	defer d.actionMapLock.Unlock()

	d.actions[id] = newAction
}

func (d *DbDaemonPlugin) deleteActionsMap(rid string) {
	d.actionMapLock.Lock()
	defer d.actionMapLock.Unlock()

	delete(d.actions, rid)
}

func (d *DbDaemonPlugin) getActionsMap(rid string) (IDbDaemonAction, bool) {
	d.actionMapLock.Lock()
	defer d.actionMapLock.Unlock()

	act, ok := d.actions[rid]
	return act, ok
}
