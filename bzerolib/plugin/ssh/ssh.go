package ssh

import (
	"net"

	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type SshAction string

const (
	DefaultSsh SshAction = "default"
)

type SshActionParams struct {
	TargetUser string `json:"targetUser"`
	RemotePort int    `json:"remotePort"`
}

type SshFood struct {
	Action SshAction
	Conn   *net.TCPConn
}

type SshOpenMessage struct {
	TargetUser           string             `json:"targetUser"`
	PublicKey            []byte             `json:"publicKey"`
	StreamMessageVersion smsg.SchemaVersion `json:"streamMessageVersion"`
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
