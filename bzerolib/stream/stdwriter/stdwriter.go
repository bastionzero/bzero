package stdwriter

import (
	"encoding/base64"

	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type StdWriter struct {
	outputChannel  chan smsg.StreamMessage
	SchemaVersion  smsg.SchemaVersion
	RequestId      string
	SequenceNumber int
	Action         string
	Type           smsg.StreamType
}

// Stdout or Stderr
func NewStdWriter(ch chan smsg.StreamMessage, schemaVersion smsg.SchemaVersion, requestId string, streamAction string, streamType smsg.StreamType) *StdWriter {
	return &StdWriter{
		outputChannel:  ch,
		SchemaVersion:  schemaVersion,
		RequestId:      requestId,
		SequenceNumber: 0,
		Action:         streamAction,
		Type:           streamType,
	}
}

func (w *StdWriter) Write(p []byte) (int, error) {
	str := base64.StdEncoding.EncodeToString(p)
	message := smsg.StreamMessage{
		SchemaVersion:  w.SchemaVersion,
		SequenceNumber: w.SequenceNumber,
		RequestId:      w.RequestId,
		Action:         string(w.Action),
		Type:           w.Type,
		Content:        str,
		More:           true, // FIXME: ?
	}
	w.outputChannel <- message
	w.SequenceNumber = w.SequenceNumber + 1

	return len(p), nil
}
