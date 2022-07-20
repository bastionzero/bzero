package exec

import (
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// Exec payload for the "kube/exec/start" action
type KubeExecStartActionPayload struct {
	RequestId            string             `json:"requestId"`
	StreamMessageVersion smsg.SchemaVersion `json:"streamMessageVersion"` // informs Agent what SchemaVersion to use
	LogId                string             `json:"logId"`
	IsStdIn              bool               `json:"isStdIn"` // Should we forward stdin to the container (-i flag in kubectl exec)
	IsTty                bool               `json:"isTty"`   // Is StdIn a TTY (-t flag in kubectl exec)
	Command              []string           `json:"command"`
	Endpoint             string             `json:"endpoint"`
	CommandBeingRun      string             `json:"commandBeingRun"`
}

// Exec payload for the "kube/exec/stop" action
type KubeExecStopActionPayload struct {
	RequestId string `json:"requestId"`
	LogId     string `json:"logId"`
	// (optional) informs Agent what SchemaVersion to use
	StreamMessageVersion smsg.SchemaVersion `json:"streamMessageVersion"`
}

// Exec payload for the "kube/exec/input" action
type KubeStdinActionPayload struct {
	RequestId string `json:"requestId"`
	LogId     string `json:"logId"`
	Stdin     []byte `json:"stdin"`
}

// payload for "kube/exec/resize"
type KubeExecResizeActionPayload struct {
	RequestId string `json:"requestId"`
	LogId     string `json:"logId"`
	Width     uint16 `json:"width"`
	Height    uint16 `json:"height"`
}
