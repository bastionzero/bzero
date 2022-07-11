/*
This package defines the messages needed to unwrap and rewrap SignalR messages.
We've abstracted this wrapper so that we can move away from SignalR in the future,
and not have to reinvent our message structure.
*/
package websocket

import "encoding/json"

type SignalRWebsocketMethod string

const (
	// SignalR Constants
	signalRMessageTerminatorByte = 0x1E
)

type SignalRMessageType int

const (
	// Ref: https://github.com/aspnet/SignalR/blob/master/specs/HubProtocol.md#invocation-message-encoding
	Invocation SignalRMessageType = 1
	// Ref: https://github.com/aspnet/SignalR/blob/master/specs/HubProtocol.md#completion-message-encoding
	Completion SignalRMessageType = 3
)

type SignalRNegotiateResponse struct {
	NegotiateVersion int
	ConnectionId     string
}

// This is our SignalR wrapper, every message that comes in thru
// the datachannel will be sent using SignalR, so we have to be
// able to unwrap and re-wrap it.  The AgentMessage is our generic
// message for everything we care about.

type SignalRMessageTypeOnly struct {
	Type int `json:"type"`
}
type SignalRInvocationMessage struct {
	Type         int                    `json:"type"`
	Target       SignalRWebsocketMethod `json:"target"` // hub name
	Arguments    []json.RawMessage      `json:"arguments"`
	InvocationId *string                `json:"invocationId,omitempty"`
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
