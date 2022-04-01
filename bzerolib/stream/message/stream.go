package message

type SchemaVersion string

const (
	CurrentSchema SchemaVersion = "202203"
)

// Agent Output Streaming Messages

type StreamMessage struct {
	RequestId      string        `json:"requestId"`     // TODO: deprecated as of schemaVersion 202203
	SchemaVersion  SchemaVersion `json:"schemaVersion"` // new as of schemaVersion 202203
	SequenceNumber int           `json:"sequenceId"`
	Action         string        `json:"action"` // new as of schemaVersion 202203
	Type           StreamType    `json:"type"`   // either stdout or stderr, see "StreamType"
	TypeV2         StreamType    `json:"typeV2"` // temporarily used while we transitioned to a versioned schema
	More           bool          `json:"more"`   // new as of schemaVersion 202203
	Content        string        `json:"content"`
}

// Type restriction on our different kinds of agent
// output streams. StdIn will come in the form of a
// Keysplitting DataMessage
type StreamType string

const (
	StdErrV2 StreamType = "stderr"
	StdOutV2 StreamType = "stdout"

	Data  StreamType = "data"
	Error StreamType = "error"

	Start  StreamType = "start"
	Stop   StreamType = "stop"
	Stream StreamType = "stream"

	DataOutV2   StreamType = "dataout"
	AgentStopV2 StreamType = "agentstop"

	Ready StreamType = "ready"
)

const (
	StdErr StreamType = "kube/exec/stderr"
	StdOut StreamType = "kube/exec/stdout"

	LogOut StreamType = "kube/log/stdout"

	PortForwardData  StreamType = "kube/portforward/data"
	PortForwardError StreamType = "kube/portforward/error"

	DbStream    StreamType = "db/stream"
	DbStreamEnd StreamType = "db/stream/end"

	WebError     StreamType = "web/error"
	WebStream    StreamType = "web/stream"
	WebStreamEnd StreamType = "web/stream/end"

	DataOut   StreamType = "web/websocket/dataout"
	AgentStop StreamType = "web/websocket/agentstop"

	StreamData  StreamType = "kube/stream/stdout"
	StreamStart StreamType = "kube/stream/start"
	StreamStop  StreamType = "kube/stream/stop"
	StreamEnd   StreamType = "kube/stream/end"
)
