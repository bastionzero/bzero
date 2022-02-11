package db

import "net"

type DbAction string

const (
	Dial DbAction = "dial"
)

type DbActionParams struct {
	RemotePort int    `json:"remotePort"`
	RemoteHost string `json:"remoteHost"`
}

type DbFood struct {
	Action DbAction
	Conn   *net.TCPConn
}
