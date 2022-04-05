package webwebsocket

type WebWebsocketSubAction string

const (
	Start      WebWebsocketSubAction = "web/websocket/start"
	DataIn     WebWebsocketSubAction = "web/websocket/datain"
	DaemonStop WebWebsocketSubAction = "web/websocket/daemonstop"
)

type WebWebsocketStartActionPayload struct {
	RequestId string              `json:"requestId"`
	Endpoint  string              `json:"endpoint"`
	Headers   map[string][]string `json:"headers"`
	Method    string              `json:"method"`
}

type WebWebsocketDataInActionPayload struct {
	RequestId   string `json:"requestId"`
	Message     string `json:"message"`
	MessageType int    `json:"messageType"`
}

type WebWebsocketStreamDataOut struct {
	Message     string `json:"message"`
	MessageType int    `json:"messageType"`
}

type WebWebsocketDaemonStopActionPayload struct {
	RequestId string `json:"requestId"`
}
