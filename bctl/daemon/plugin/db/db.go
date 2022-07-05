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
	ReceiveStream(stream smsg.StreamMessage)
	Start(lconn *net.TCPConn) error
	Done() <-chan struct{}
	Kill()
}

type DbDaemonPlugin struct {
	logger *logger.Logger

	action   IDbDaemonAction
	doneChan chan struct{}
	killed   bool

	// outbox
	outboxQueue chan plugin.ActionWrapper

	// Db-specific vars
	sequenceNumber int
}

func New(logger *logger.Logger) *DbDaemonPlugin {
	return &DbDaemonPlugin{
		logger:         logger,
		doneChan:       make(chan struct{}),
		killed:         false,
		outboxQueue:    make(chan plugin.ActionWrapper, 5),
		sequenceNumber: 0,
	}
}

func (d *DbDaemonPlugin) StartAction(action bzdb.DbAction, conn *net.TCPConn) error {
	if d.killed {
		return fmt.Errorf("plugin has already been killed, cannot create a new shell action")
	}

	requestId := uuid.New().String()
	actLogger := d.logger.GetActionLogger(string(action))

	switch action {
	case bzdb.Dial:
		d.action = dial.New(actLogger, requestId, d.outboxQueue, d.doneChan)
	default:
		return fmt.Errorf("unrecognized db action: %s", action)
	}

	d.logger.Infof("db plugin created %s action", action)

	// send local tcp connection to action
	if err := d.action.Start(conn); err != nil {
		return fmt.Errorf("%s error: %s", action, err)
	}

	return nil
}

func (d *DbDaemonPlugin) Kill() {
	d.killed = true
	if d.action != nil {
		d.action.Kill()
	}
}

func (d *DbDaemonPlugin) Done() <-chan struct{} {
	return d.doneChan
}

func (d *DbDaemonPlugin) Outbox() <-chan plugin.ActionWrapper {
	return d.outboxQueue
}

func (d *DbDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	d.logger.Infof("db plugin received %v stream", smessage.Type)

	if d.action != nil {
		d.action.ReceiveStream(smessage)
	} else {
		d.logger.Debugf("db plugin received a stream message before an action was created. Ignoring")
	}
}

func (d *DbDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) error {
	d.logger.Debugf("Received a keysplitting message with action: %s", action)

	// the only keysplitting message that we would receive is the ack from the agent after stopping the dial action
	// we don't do anything with it on the daemon side, so we receive it here and it will get logged
	// but no particular action will be taken
	return nil
}
