package connectionhub

import (
	"fmt"

	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/connections/broadcast"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"gopkg.in/tomb.v2"
)

const (
	// Send Methods
	ResponseAgentToBastionV1 = "ResponseAgentToBastionV1"
	CloseDaemonWebsocketV1   = "CloseDaemonWebsocketV1"

	// Receive Methods
	RequestBastionToAgentV1 = "RequestBastionToAgentV1"
)

type ConnectionHub struct {
	logger *logger.Logger
	tmb    tomb.Tomb

	doneChan chan struct{}

	inbound chan *am.AgentMessage

	broadcaster broadcast.Broadcaster
}

func New(logger *logger.Logger, parentTmb *tomb.Tomb) *ConnectionHub {

	conn := ConnectionHub{
		logger:      logger,
		doneChan:    make(chan struct{}),
		inbound:     make(chan *am.AgentMessage),
		broadcaster: broadcast.New(),
	}

	conn.tmb.Go(func() error {
		for {
			select {
			case <-parentTmb.Dying():
				logger.Infof("agent connection was orphaned too young and can't be batman :'(")
				return nil
			case message := <-conn.inbound:
				conn.processInbound(*message)
			}
		}
	})

	return &conn
}

func (c *ConnectionHub) Close() {}

func (c *ConnectionHub) Done() <-chan struct{} {
	return c.doneChan
}

func (c *ConnectionHub) processInbound(message am.AgentMessage) {

}

func (c *ConnectionHub) Receive(agentMessage am.AgentMessage) {
	c.inbound <- &agentMessage
}

func (c *ConnectionHub) Attach(id string, channel broadcast.IChannel) {
	c.broadcaster.Subscribe(id, channel)
}

func (c *ConnectionHub) Detach(id string) {
	c.broadcaster.Unsubscribe(id)
}

func (c *ConnectionHub) Send(message am.AgentMessage) {
	// Keysplitting ack messages are only sent to their original sender
	// stream messages are broadcast
	c.broadcaster.Broadcast(message)
}

// LUCIE: this function is needed for attachment need to figure out
// a way to identify a channel by bzcert
// func (c *ConnectionHub) Narrowcast(message am.AgentMessage) {
// 	id := ""
// 	c.broadcaster.Narrowcast(id, message)
// }

// agent's data channel function to select signalR hub method based on agent message type
func targetSelector(agentMessage am.AgentMessage) (string, error) {
	switch am.MessageType(agentMessage.MessageType) {
	case am.CloseDaemonWebsocket:
		return CloseDaemonWebsocketV1, nil
	case am.Keysplitting, am.Stream, am.Error:
		return ResponseAgentToBastionV1, nil
	default:
		return "", fmt.Errorf("unable to determine target for message type: %s", agentMessage.MessageType)
	}
}
