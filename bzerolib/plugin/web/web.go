package web

type WebAction string

const (
	Dial      WebAction = "dial"
	Websocket WebAction = "websocket"
)

type WebActionParams struct {
	RemotePort int
	RemoteHost string
}
