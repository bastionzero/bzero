package webwebsocket

type WebWebsocketStartActionPayload struct {
	RequestId string              `json:"requestId"`
	Endpoint  string              `json:"endpoint"`
	Headers   map[string][]string `json:"headers"`
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
