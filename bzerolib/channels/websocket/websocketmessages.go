package websocket

// Daemon DataChannel Websocket Methods
const (
	// Send Methods
	OpenDataChannelDaemonToBastionV1  SignalRWebsocketMethod = "OpenDataChannelDaemonToBastionV1"
	CloseDataChannelDaemonToBastionV1 SignalRWebsocketMethod = "CloseDataChannelDaemonToBastionV1"
	RequestDaemonToBastionV1          SignalRWebsocketMethod = "RequestDaemonToBastionV1"

	// Receive Methods
	ResponseBastionToDaemonV1 SignalRWebsocketMethod = "ResponseBastionToDaemonV1"
	AgentConnected            SignalRWebsocketMethod = "AgentConnected"
	DaemonCloseConnection     SignalRWebsocketMethod = "CloseConnection"
)

// Agent Control Channel Websocket Methods
const (
	// Send Methods
	AliveCheckAgentToBastion SignalRWebsocketMethod = "AliveCheckAgentToBastion"

	// Receive Methods
	OpenWebsocket            SignalRWebsocketMethod = "OpenWebsocket"
	CloseWebsocket           SignalRWebsocketMethod = "CloseWebsocket"
	OpenDataChannel          SignalRWebsocketMethod = "OpenDataChannel"
	CloseDataChannel         SignalRWebsocketMethod = "CloseDataChannel"
	AliveCheckBastionToAgent SignalRWebsocketMethod = "AliveCheckBastionToAgent"
	AgentCloseConnection     SignalRWebsocketMethod = "CloseConnection"
	ShutDown                 SignalRWebsocketMethod = "ShutDown"
)

// Agent DataChannel Websocket Methods
const (
	// Send Methods
	ResponseAgentToBastionV1 SignalRWebsocketMethod = "ResponseAgentToBastionV1"
	CloseDaemonWebsocketV1   SignalRWebsocketMethod = "CloseDaemonWebsocketV1"

	// Receive Methods
	RequestBastionToAgentV1 SignalRWebsocketMethod = "RequestBastionToAgentV1"
)

// Websocket message types used directly by Connection Node (not contained within AgentMessage)
type AgentConnectedMessage struct {
	ConnectionId string `json:"connectionId"`
}
