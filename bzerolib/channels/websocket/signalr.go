/*
This package defines the messages needed to unwrap and rewrap SignalR messages.
We've abstracted this wrapper so that we can move away from SignalR in the future,
and not have to reinvent our message structure.
*/
package websocket

import am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"

type SignalRNegotiateResponse struct {
	NegotiateVersion int
	ConnectionId     string
}

// This is our SignalR wrapper, every message that comes in thru
// the data channel will be sent using SignalR, so we have to be
// able to unwrap and re-wrap it.  The AgentMessage is our generic
// message for everything we care about.

type SignalRMessageTypeOnly struct {
	Type int `json:"type"`
}
type SignalRInvocationMessage struct {
	Type         int               `json:"type"`
	Target       string            `json:"target"` // hub name
	Arguments    []am.AgentMessage `json:"arguments"`
	InvocationId *string           `json:"invocationId,omitempty"`
}

type SignalRCompletionMessage struct {
	Type         int                `json:"type"`
	InvocationId *string            `json:"invocationId"`
	Result       *WebsocketResponse `json:"result"`
	Error        *string            `json:"error"`
}
type WebsocketResponse struct {
	ErrorMessage *string `json:"errorMessage"`
	Error        bool    `json:"error"`
}

// This is our close message struct
type CloseMessage struct {
	Message string `json:"message"`
}

// Message definitions for provisioning websocket
type ProvisionDataChannel struct {
	DataChannelId string `json:"dataChannelId"`
	Role          string `json:"role"`
	Action        string `json:"action"`
}
