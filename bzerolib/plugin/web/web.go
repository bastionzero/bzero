package web

import "net"

type WebAction string

const (
	Dial WebAction = "dial"
	// Start  WebAction = "start"
	// DataIn WebAction = "input"
)

type WebActionParams struct {
	TargetPort     int
	TargetHost     string
	TargetHostName string
}

type WebFood struct {
	Action WebAction
	Conn   *net.TCPConn
}
