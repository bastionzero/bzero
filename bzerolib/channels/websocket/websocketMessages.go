package websocket

type CreateConnectionRequest struct {
	ServerType int    `json:"serverType"`
	ServerId   string `json:"serverId"`
	SessionId  string `json:"sessionId"`
	Username   string `json:"username"`
}
