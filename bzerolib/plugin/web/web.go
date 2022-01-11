package web

import "net"

type WebAction string

const (
	Dial WebAction = "dial"
	// Start  WebAction = "start"
	// DataIn WebAction = "input"
)

type WebActionParams struct {
	RemotePort int
	RemoteHost string
}

type WebFood struct {
	Action WebAction
	Conn   *net.TCPConn
}
