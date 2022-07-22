package connection

import (
	"bastionzero.com/bctl/v1/bzerolib/connections/broadcast"
	"gopkg.in/tomb.v2"
)

type Connection struct {
	tmb tomb.Tomb

	broadcaster broadcast.Broadcaster
}

func New() *Connection {
	return &Connection{
		broadcaster: broadcast.New(),
	}
}

func (c *Connection) Attach(id string, channel broadcast.IChannel) {
	c.broadcaster.Subscribe(id, channel)
}

func (c *Connection) Detach(id string) {
	c.broadcaster.Unsubscribe(id)
}
