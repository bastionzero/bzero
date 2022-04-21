package db

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/google/uuid"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/db/actions/dial"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzdb "bastionzero.com/bctl/v1/bzerolib/plugin/db"
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

	action IDbDaemonAction

	// Db-specific vars
	sequenceNumber int
}

func New(parentTmb *tomb.Tomb, logger *logger.Logger, actionParams bzdb.DbActionParams) (*DbDaemonPlugin, error) {
	plugin := DbDaemonPlugin{
		tmb:             parentTmb,
		logger:          logger,
		streamInputChan: make(chan smsg.StreamMessage, 25),
		outputQueue:     make(chan plugin.ActionWrapper, 25),
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
				if err := plugin.processStream(streamMessage); err != nil {
					plugin.logger.Error(err)
				}
			}
		}
	}()

	return &plugin, nil
}

func (d *DbDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	d.logger.Debugf("db plugin received %v stream", smessage.Type)
	d.streamInputChan <- smessage
}

func (d *DbDaemonPlugin) processStream(smessage smsg.StreamMessage) error {
	if d.action != nil {
		d.action.ReceiveStream(smessage)
		return nil
	} else {
		return fmt.Errorf("db plugin received stream message before an action was created. Ignoring")
	}
}

func (d *DbDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) (string, []byte, error) {
	d.logger.Infof("Received a keysplitting message with action: %s", action)
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
	d.logger.Infof("Db plugin received keysplitting message with action: %s", action)

	// currently the only keysplitting message we care about is the acknowledgement of our request for the agent to stop the dial action
	// but since we don't do anything with it we log it, make a comment, and return nil
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

	var actOutputChan chan plugin.ActionWrapper
	switch bzdb.DbAction(dbFood.Action) {
	case bzdb.Dial:
		d.action, actOutputChan = dial.New(actLogger, requestId)
	default:
		return fmt.Errorf("unrecognized db action: %v", string(dbFood.Action))
	}

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
					d.logger.Infof("Closing db %s action", dbFood.Action)
					d.tmb.Kill(fmt.Errorf("done with the only action this datachannel will ever do"))
					return
				}
			}
		}
	}()

	d.logger.Infof("Created %s action", string(dbFood.Action))

	// send local tcp connection to action
	if err := d.action.Start(d.tmb, dbFood.Conn); err != nil {
		return fmt.Errorf("%s error: %s", string(dbFood.Action), err)
	}

	return nil
}
