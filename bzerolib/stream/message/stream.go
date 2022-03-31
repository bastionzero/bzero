package message

type SchemaVersion string

const (
	CurrentSchema SchemaVersion = "202203"
)

// Agent Output Streaming Messages

type StreamMessage struct {
	SchemaVersion  SchemaVersion `json:"schemaVersion"` // new as of bzero 4.2.0 / schemaVersion 1
	SequenceNumber int           `json:"sequenceId"`
	Action         string        `json:"action"` // new as of bzero 4.2.0 / schemaVersion 1
	Type           StreamType    `json:"type"`   // either stdout or stderr, see "StreamType"
	More           bool          `json:"more"`   // new as of bzero 4.2.0 / schemaVersion 1
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
