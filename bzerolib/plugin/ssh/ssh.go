package ssh

import "net"

type SshAction string

const (
	DefaultSsh SshAction = "default"
)

type SshActionParams struct {
	TargetUser string `json:"targetUser"`
}

type SshFood struct {
	Action SshAction
	Conn   *net.TCPConn
}

type SshOpenMessage struct {
	TargetUser string `json:"targetUser"`
	PublicKey  []byte `json:"publicKey"`
}

type SshInputMessage struct {
	Data []byte `json:"data"`
}

type SshCloseMessage struct {
	Reason string `json:"reason"`
}

type SshSubAction string

const (
	SshOpen  SshSubAction = "ssh/open"
	SshInput SshSubAction = "ssh/input"
	SshClose SshSubAction = "ssh/close"
)
