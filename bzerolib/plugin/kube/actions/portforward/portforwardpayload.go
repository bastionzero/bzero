package portforward

import (
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	DataStreamBufferSize  = 1024 * 1024
	ErrorStreamBufferSize = 1024 * 1024
)

// Portforward payload for the "kube/portforward/start" action
type PortForwardStartActionPayload struct {
	RequestId            string             `json:"requestId"`
	StreamMessageVersion smsg.SchemaVersion `json:"streamMessageVersion"` // informs Agent what SchemaVersion to use
	LogId                string             `json:"logId"`
	Endpoint             string             `json:"endpoint"`
	DataHeaders          map[string]string  `json:"dataHeaders"`
	ErrorHeaders         map[string]string  `json:"errorHeaders"`
	CommandBeingRun      string             `json:"commandBeingRun"`
}

// Portforward payload for the "kube/portforward/datain" action
type PortForwardActionPayload struct {
	RequestId            string `json:"requestId"`
	LogId                string `json:"logId"`
	Data                 []byte `json:"data"`
	PortForwardRequestId string `json:"portForwardRequestId"`
	PodPort              int64  `json:"podPort"`
}

// Portforward payload for the "kube/portforward/request/stop" action
type PortForwardStopRequestActionPayload struct {
	RequestId            string `json:"requestId"`
	LogId                string `json:"logId"`
	PortForwardRequestId string `json:"portForwardRequestId"`
}

// Portforward payload for the "kube/portforward/stop" action
type PortForwardStopActionPayload struct {
	RequestId string `json:"requestId"`
	LogId     string `json:"logId"`
}

// Portforward payload for stream messages
type PortForwardStreamMessageContent struct {
	RequestId string `json:"portForwardRequestId"`
	Content   []byte `json:"content"`
}
