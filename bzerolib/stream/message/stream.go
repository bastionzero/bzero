package message

const (
	CurrentSchema string = "202203"
)

// Agent Output Streaming Messages

type StreamMessage struct {
	LogId          string `json:"logId"`         // TODO: deprecated as of bzero 4.2.0 / schemaVersion 1
	RequestId      string `json:"requestId"`     // TODO: deprecated as of bzero 4.2.0 / schemaVersion 1
	SchemaVersion  string `json:"schemaVersion"` // new as of bzero 4.2.0 / schemaVersion 1
	SequenceNumber int    `json:"sequenceId"`
	Action         string `json:"action"` // new as of bzero 4.2.0 / schemaVersion 1
	Type           string `json:"type"`   // either stdout or stderr, see "StreamType"
	More           bool   `json:"more"`   // new as of bzero 4.2.0 / schemaVersion 1
	Content        string `json:"content"`
}

// Type restriction on our different kinds of agent
// output streams.  StdIn will come in the form of a
// Keysplitting DataMessage
type StreamType string

const (
	// FIXME:
	FIXMEStdErr StreamType = "stderr"
	// FIXME:
	FIXMEStdOut StreamType = "stdout"
	// FIXME:
	FIXMEStdIn StreamType = "stdin"
	Data       StreamType = "data"
	Error      StreamType = "error"
	Start      StreamType = "start"
	Stop       StreamType = "stop"
	Stream     StreamType = "stream"
)

type StreamAction string

const (
	Db              StreamAction = "db"
	KubeExec        StreamAction = "kube/exec"
	KubeLog         StreamAction = "kube/log"
	KubePortForward StreamAction = "kube/portforward"
	KubeStream      StreamAction = "kube/stream"
	Web             StreamAction = "web"
)

// TODO: remove, but keep them for now so that code will compile
const (
	StdErr           StreamType = "kube/exec/stderr"
	StdOut           StreamType = "kube/exec/stdout"
	StdIn            StreamType = "kube/exec/stdin"
	LogOut           StreamType = "kube/log/stdout"
	PortForwardData  StreamType = "kube/portforward/data"
	PortForwardError StreamType = "kube/portforward/error"
	DbStream         StreamType = "db/stream"
	DbStreamEnd      StreamType = "db/stream/end"
	WebError         StreamType = "web/error"
	WebStream        StreamType = "web/stream"
	WebStreamEnd     StreamType = "web/stream/end"
	StreamData       StreamType = "kube/stream/stdout"
	StreamStart      StreamType = "kube/stream/start"
	StreamStop       StreamType = "kube/stream/stop"
	StreamEnd        StreamType = "kube/stream/end"
)
