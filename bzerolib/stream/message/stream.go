package message

// Agent Output Streaming Messages

type StreamMessage struct {
	Type           string `json:"type"` // either stdout or stderr, see "StreamType"
	LogId          string `json:"logId"`
	RequestId      string `json:"requestId"`
	SequenceNumber int    `json:"sequenceId"`
	Content        string `json:"content"`
}

// Type restriction on our different kinds of agent
// output streams.  StdIn will come in the form of a
// Keysplitting DataMessage
type StreamType string

const (
	StdErr StreamType = "kube/exec/stderr"
	StdOut StreamType = "kube/exec/stdout"
	StdIn  StreamType = "kube/exec/stdin"

	LogOut StreamType = "kube/log/stdout"

	PortForwardData  StreamType = "kube/portforward/data"
	PortForwardError StreamType = "kube/portforward/error"
	PortForwardEnd   StreamType = "kube/portforward/End"

	DbStream    StreamType = "db/stream"
	DbStreamEnd StreamType = "db/stream/end"

	WebError     StreamType = "web/error"
	WebStream    StreamType = "web/stream"
	WebStreamEnd StreamType = "web/stream/end"

	StreamData  StreamType = "kube/stream/stdout"
	StreamStart StreamType = "kube/stream/start"
	StreamStop  StreamType = "kube/stream/stop"
	StreamEnd   StreamType = "kube/stream/end"
)
