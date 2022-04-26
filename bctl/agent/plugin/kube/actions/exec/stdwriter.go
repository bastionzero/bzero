package exec

import (
	"encoding/base64"

	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type StdWriter struct {
	outputChannel        chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion
	RequestId            string
	SequenceNumber       int
	Action               string
	logId                string
	Type                 smsg.StreamType
}

// Stdout or Stderr
func NewStdWriter(ch chan smsg.StreamMessage, streamMessageVersion smsg.SchemaVersion, requestId string, streamAction string, streamType smsg.StreamType, logId string) *StdWriter {
	return &StdWriter{
		outputChannel:        ch,
		streamMessageVersion: streamMessageVersion,
		RequestId:            requestId,
		SequenceNumber:       0,
		Action:               streamAction,
		Type:                 streamType,
		logId:                logId,
	}
}

func (w *StdWriter) Write(p []byte) (int, error) {
	str := base64.StdEncoding.EncodeToString(p)
	message := smsg.StreamMessage{
		SchemaVersion:  w.streamMessageVersion,
		SequenceNumber: w.SequenceNumber,
		RequestId:      w.RequestId,
		Action:         string(w.Action),
		Type:           w.Type,
		LogId:          w.logId,
		Content:        str,
		More:           true,
	}
	w.outputChannel <- message
	w.SequenceNumber = w.SequenceNumber + 1

	return len(p), nil
}
