package ssh

import (
	"fmt"

	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type SshAction string

const (
	OpaqueSsh      SshAction = "opaque"
	TransparentSsh SshAction = "transparent"
	scpWithSpace   string    = "scp "
)

type SshActionParams struct {
	TargetUser string `json:"targetUser"`
	RemoteHost string `json:"remoteHost"`
	RemotePort int    `json:"remotePort"`
}

type SshOpenMessage struct {
	TargetUser           string             `json:"targetUser"`
	PublicKey            []byte             `json:"publicKey"`
	StreamMessageVersion smsg.SchemaVersion `json:"streamMessageVersion"`
}

type SshInputMessage struct {
	Data []byte `json:"data"`
}

type SshExecMessage struct {
	Command string `json:"command"`
}

type SshCloseMessage struct {
	Reason string `json:"reason"`
}

type SshSubAction string

const (
	SshOpen  SshSubAction = "ssh/open"
	SshExec  SshSubAction = "ssh/exec"
	SshInput SshSubAction = "ssh/input"
	SshClose SshSubAction = "ssh/close"
)

func IsValidScp(command string) bool {
	return string([]rune(command)[:4]) == scpWithSpace
}

func UnauthorizedCommandError(received string) string {
	return fmt.Sprintf("unauthorized command: this user is only allowed to perform file transfer via scp, but recieved %s", received)
}
