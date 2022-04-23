package db

import (
	"fmt"
	"net"

	"github.com/google/uuid"

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
	Kill()
}

type DbDaemonPlugin struct {
	logger *logger.Logger

	// outbox
	outputQueue chan plugin.ActionWrapper

	action IDbDaemonAction

	// Db-specific vars
	sequenceNumber int
}

func New(logger *logger.Logger, actionParams interface{}) (*DbDaemonPlugin, error) {
	plugin := DbDaemonPlugin{
		logger:         logger,
		outputQueue:    make(chan plugin.ActionWrapper, 5),
		sequenceNumber: 0,
	}

	return &plugin, nil
}

func (d *DbDaemonPlugin) Kill() {
	if d.action != nil {
		d.action.Kill()
	}
}

func (d *DbDaemonPlugin) Done() <-chan struct{} {
	if d.action != nil {
		return d.action.Done()
	} else {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
}

func (d *DbDaemonPlugin) Outbox() <-chan plugin.ActionWrapper {
	return d.outputQueue
}

func (d *DbDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	d.logger.Debugf("db plugin received %v stream", smessage.Type)

	if d.action != nil {
		d.action.ReceiveStream(smessage)
	} else {
		d.logger.Debugf("db plugin received stream message before an action was created. Ignoring")
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

	requestId := uuid.New().String()
	actLogger := d.logger.GetActionLogger(string(dbFood.Action))

	switch bzdb.DbAction(dbFood.Action) {
	case bzdb.Dial:
		d.action = dial.New(actLogger, requestId, d.outputQueue)
	default:
		return fmt.Errorf("unrecognized db action: %v", string(dbFood.Action))
	}

	d.logger.Infof("db plugin created %s action", string(dbFood.Action))

	// send local tcp connection to action
	if err := d.action.Start(dbFood.Conn); err != nil {
		return fmt.Errorf("%s error: %s", string(dbFood.Action), err)
	}

	return nil
}
