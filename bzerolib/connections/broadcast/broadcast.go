package broadcast

import (
	"sync"

	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
)

type IChannel interface {
	Receive(agentMessage am.AgentMessage)
	Close(reason error)
}

type Broadcaster interface {
	Subscribe(id string, subscriber IChannel)
	Unsubscribe(id string)
	Broadcast(message am.AgentMessage)
}

type Broadcast struct {
	subscribers map[string]IChannel
	lock        sync.Mutex
}

func New() *Broadcast {
	return &Broadcast{
		subscribers: map[string]IChannel{},
	}
}

func (b *Broadcast) Subscribe(id string, subscriber IChannel) {
	b.lock.Lock()
	defer b.lock.Unlock()

	b.subscribers[id] = subscriber
}

func (b *Broadcast) Unsubscribe(id string) {
	b.lock.Lock()
	defer b.lock.Unlock()

	delete(b.subscribers, id)
}

// Allows for broadcasting to any number of IChannels, should we be blocking
// until someone's listening
func (b *Broadcast) Broadcast(message am.AgentMessage) {
	b.lock.Lock()
	defer b.lock.Unlock()

	for _, channel := range b.subscribers {
		if channel == nil {
			continue
		}

		channel.Receive(message)
	}
}
