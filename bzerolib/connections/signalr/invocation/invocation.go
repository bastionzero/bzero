package invocation

import (
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
)

type Invocator struct {
}

type Invocation struct {
	// Map of sent messages for which we're awaiting CompletionMessages
	// keyed by InvocationId
	messagesWaitingResponse map[string]am.AgentMessage

	// Counter for generating invocationIds which are sequential
	// LUCIE: it's int64 in websocket.go but it doesn't look like it needs to be
	invocationIdCounter int
}

func New()
