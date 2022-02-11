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
	ConnectionId         string `json:"connectionId"`
	ConnectionNodeId     string `json:"connectionNodeId"`
	ConnectionServiceUrl string `json:"connectionServiceUrl"`
	Token                string `json:"token"`
	Type                 string `json:"type"`
}

type CloseWebsocketMessage struct {
	ConnectionId string `json:"connectionId"`
}

type OpenDataChannelMessage struct {
	ConnectionId  string `json:"connectionId"`
	DataChannelId string `json:"dataChannelId"`
	Syn           []byte `json:"syn"`
}

type CloseDataChannelMessage struct {
	DataChannelId string `json:"dataChannelId"`
	ConnectionId  string `json:"connectionId"`
}
