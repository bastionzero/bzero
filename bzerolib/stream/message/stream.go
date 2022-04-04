package message

// YYYYMM-formatted string -- the earliest this schema was used
type SchemaVersion string

const (
	CurrentSchema SchemaVersion = "202204" // if the agent and daemon are both up to date, they send/receive messages with this version
)

// Agent Output Streaming Messages
type StreamMessage struct {
	RequestId      string        `json:"requestId"`     // deprecated / scheduled for removal as of schemaVersion 202204
	SchemaVersion  SchemaVersion `json:"schemaVersion"` // new as of schemaVersion 202204
	SequenceNumber int           `json:"sequenceId"`
	Action         string        `json:"action"` // new as of schemaVersion 202204
	Type           StreamType    `json:"type"`   // either stdout or stderr, see "StreamType"
	TypeV2         StreamType    `json:"typeV2"` // temporarily used while we transitioned to a versioned schema
	More           bool          `json:"more"`   // new as of schemaVersion 202204
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

	Ready StreamType = "ready"
)

const (
	StdErr StreamType = "kube/exec/stderr"
	StdOut StreamType = "kube/exec/stdout"

	LogOut StreamType = "kube/log/stdout"

	ReadyPortForward StreamType = "kube/portforward/ready"
	DataPortForward  StreamType = "kube/portforward/data"
	ErrorPortForward StreamType = "kube/portforward/error"

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
