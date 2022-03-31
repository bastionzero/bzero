package message

type SchemaVersion string

const (
	CurrentSchema SchemaVersion = "202203"
)

// Agent Output Streaming Messages

type StreamMessage struct {
	RequestId      string        `json:"requestId"`     // TODO: deprecated as of schemaVersion 202203
	SchemaVersion  SchemaVersion `json:"schemaVersion"` // new as of bzero 4.2.0 / schemaVersion 202203
	SequenceNumber int           `json:"sequenceId"`
	Action         string        `json:"action"` // new as of bzero 4.2.0 / schemaVersion 202203
	Type           StreamType    `json:"type"`   // either stdout or stderr, see "StreamType"
	More           bool          `json:"more"`   // new as of bzero 4.2.0 / schemaVersion 202203
	Content        string        `json:"content"`
}

// Type restriction on our different kinds of agent
// output streams. StdIn will come in the form of a
// Keysplitting DataMessage
type StreamType string

const (
	StdErr StreamType = "stderr"
	StdOut StreamType = "stdout"
	StdIn  StreamType = "stdin"

	Data  StreamType = "data"
	Error StreamType = "error"

	Start  StreamType = "start"
	Stop   StreamType = "stop"
	Stream StreamType = "stream"

	DataOut   StreamType = "dataout"
	AgentStop StreamType = "agentstop"

	Ready StreamType = "ready"
)
