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
	return fmt.Sprint(i.counter)
}

// The only reason this works is because SignalR processes in series, if we
// ever wanted SignalR to process in parallel, we would need a much safer
// mechanism to ensure we were giving unique invocationIds
func (i *Invocation) Track(message am.AgentMessage) {
	i.trackedMessagesLock.Lock()
	defer i.trackedMessagesLock.Unlock()

	i.trackedMessages[fmt.Sprint(i.counter)] = message

	i.counter += 1
}
