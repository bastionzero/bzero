package invocation

import (
	"fmt"
	"sync"

	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
)

type Invocator interface {
	Match(id string) (am.AgentMessage, bool)
	Track(message am.AgentMessage) string
}

type Invocation struct {
	// Map of sent messages for which we're awaiting CompletionMessages
	// keyed by InvocationId
	trackedMessages     map[string]am.AgentMessage
	trackedMessagesLock sync.Mutex

	// Counter for generating invocationIds which are sequential
	// LUCIE: it's int64 in websocket.go but it doesn't look like it needs to be
	counter int
}

func New() *Invocation {
	return &Invocation{
		trackedMessages: make(map[string]am.AgentMessage),
	}
}

func (i *Invocation) Match(id string) (am.AgentMessage, bool) {
	i.trackedMessagesLock.Lock()
	defer i.trackedMessagesLock.Unlock()

	message, ok := i.trackedMessages[id]
	if ok {
		delete(i.trackedMessages, id)
	}

	return message, ok
}

func (i *Invocation) GetInvocationId() string {
	invocationId := fmt.Sprint(i.counter)
	i.counter++

	return invocationId
}

// Invocation does not promise strictly increasing Invocation IDs
// becuase messages can fail between getting the ID and Tracking
func (i *Invocation) Track(id string, message am.AgentMessage) {
	i.trackedMessagesLock.Lock()
	defer i.trackedMessagesLock.Unlock()

	i.trackedMessages[id] = message
}
