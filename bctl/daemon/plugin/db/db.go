package db

import (
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
	Start(lconn *net.TCPConn) error
	Done() <-chan struct{}
	Stop()
}

type DbDaemonPlugin struct {
	tmb    *tomb.Tomb
	logger *logger.Logger

	// Input and output channels
	streamInputChan chan smsg.StreamMessage
	outputQueue     chan plugin.ActionWrapper

	// Channel for letting the datachannel know we're done
	doneChan chan struct{}

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
		doneChan:        make(chan struct{}),
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

func (d *DbDaemonPlugin) Stop() {
	if d.action == nil {
		return
	} else {
		d.action.Stop()
	}
}

func (d *DbDaemonPlugin) Done() <-chan struct{} {
	return d.doneChan
}

func (d *DbDaemonPlugin) Outbox() <-chan plugin.ActionWrapper {
	return d.outputQueue
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

func (d *DbDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) error {
	d.logger.Debugf("Received a keysplitting message with action: %s", action)
	// the only keysplitting message that we would receive is the ack from the agent after stopping the dial action
	// we don't do anything with it on the daemon side, so we receive it here and it will get logged
	// but no particular action will be taken
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

	switch bzdb.DbAction(dbFood.Action) {
	case bzdb.Dial:
		d.action = dial.New(actLogger, requestId)
	default:
		return fmt.Errorf("unrecognized db action: %v", string(dbFood.Action))
	}

	go func() {
		select {
		case <-d.tmb.Dying():
			return
		case <-d.action.Done():
			// if the action is done, then so are we
			close(d.doneChan)
			return
		}
	}()

	d.logger.Infof("Created %s action", string(dbFood.Action))

	// send local tcp connection to action
	if err := d.action.Start(dbFood.Conn); err != nil {
		return fmt.Errorf("%s error: %s", string(dbFood.Action), err)
	}

	return nil
}
