package websocket

import am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"

type IChannel interface {
	Receive(agentMessage am.AgentMessage)
	Close(reason error)
}
