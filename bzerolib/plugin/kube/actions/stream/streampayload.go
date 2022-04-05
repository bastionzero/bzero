package stream

import (
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// For "kube/stream/..." actions

type KubeStreamActionPayload struct {
	Endpoint             string              `json:"endpoint"`
	Headers              map[string][]string `json:"headers"`
	Method               string              `json:"method"`
	Body                 string              `json:"body"`
	RequestId            string              `json:"requestId"`
	StreamMessageVersion smsg.SchemaVersion  `json:"streamMessageVersion"` // informs Agent what SchemaVersion to use
	LogId                string              `json:"logId"`
	CommandBeingRun      string              `json:"commandBeingRun"`
}

type KubeStreamHeadersPayload struct {
	Headers map[string][]string
}
