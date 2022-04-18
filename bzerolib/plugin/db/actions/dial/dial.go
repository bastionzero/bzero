package dial

import (
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type DialSubAction string

const (
	DialStart DialSubAction = "db/dial/start"
	DialInput DialSubAction = "db/dial/input"
	DialStop  DialSubAction = "db/dial/stop"
)

type DialActionPayload struct {
	RequestId string `json:"requestId"`
	// (optional) informs Agent what SchemaVersion to use
	StreamMessageVersion smsg.SchemaVersion `json:"streamMessageVersion"`
}

type DialInputActionPayload struct {
	RequestId      string `json:"requestId"`
	SequenceNumber int    `json:"sequenceNumber"`
	Data           string `json:"data"`
}
