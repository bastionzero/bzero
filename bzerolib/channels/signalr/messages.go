package signalr

import "encoding/json"

// Byte to indicate the end of a SignalR message
const signalRMessageTerminatorByte = 0x1E

type SignalRMessageType int

const (
	// Ref: https://github.com/aspnet/SignalR/blob/master/specs/HubProtocol.md#invocation-message-encoding
	Invocation SignalRMessageType = 1
	// Ref: https://github.com/aspnet/SignalR/blob/master/specs/HubProtocol.md#completion-message-encoding
	Completion SignalRMessageType = 3
)

type MessageTypeOnly struct {
	Type int `json:"type"`
}

// LUCIE: why are any of these pointers? And what does that mean?
type CompletionMessage struct {
	Type         int            `json:"type"`
	InvocationId *string        `json:"invocationId"`
	Result       *ResultMessage `json:"result"`
	Error        *string        `json:"error"`
}

type ResultMessage struct {
	ErrorMessage *string `json:"errorMessage"`
	Error        bool    `json:"error"`
}

type SignalRMessage struct {
	Type         int               `json:"type"`
	Target       string            `json:"target"` // hub name
	Arguments    []json.RawMessage `json:"arguments"`
	InvocationId *string           `json:"invocationId,omitempty"`
}
