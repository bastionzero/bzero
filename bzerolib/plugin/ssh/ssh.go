package ssh

import "net"

type SshAction string

const (
	DefaultSsh SshAction = "default"
)

type SshActionParams struct{}

type SshFood struct {
	Action SshAction
	Conn   *net.TCPConn
}

type SshOpenMessage struct {
	TargetUser string `json:"targetUser"`
}

type SshInputMessage struct {
	SequenceNumber int    `json:"sequenceNumber"`
	Data           string `json:"data"`
}

type SshCloseMessage struct{}

type SshSubAction string

const (
	SshOpen  SshSubAction = "ssh/open"
	SshInput SshSubAction = "ssh/input"
	SshClose SshSubAction = "ssh/close"
)
