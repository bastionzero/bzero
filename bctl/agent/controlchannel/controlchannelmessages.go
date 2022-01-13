package controlchannel

type AliveCheckAgentToBastionMessage struct {
	Alive        bool     `json:"alive"`
	ClusterUsers []string `json:"clusterUsers"`
}
type HealthCheckMessage struct {
	TargetName string `json:"targetName"`
}

// websocket and datachannel management payloads
type OpenWebsocketMessage struct {
	DaemonWebsocketId string `json:"daemonWebsocketId"`
	ConnectionNodeId  string `json:"connectionNodeId"`
	Token             string `json:"token"`
	Type              int    `json:"type"`
}

type CloseWebsocketMessage struct {
	DaemonWebsocketId string `json:"daemonWebsocketId"`
}

type OpenDataChannelMessage struct {
	DataChannelId     string `json:"dataChannelId"`
	DaemonWebsocketId string `json:"daemonWebsocketId"`
	Syn               []byte `json:"syn"`
}

type CloseDataChannelMessage struct {
	DataChannelId string `json:"dataChannelId"`
	ConnectionId  string `json:"connectionId"`
}

type DataChannelReadyMessage struct {
	DataChannelId string `json:"dataChannelId"`
	ConnectionId  string `json:"connectionId"`
}
