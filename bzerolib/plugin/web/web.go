package web

import "net"

type WebAction string

const (
	Dial WebAction = "dial"
)

type WebActionParams struct {
	RemotePort int
	RemoteHost string
}

type WebFood struct {
	Action WebAction
	Conn   *net.TCPConn
}
