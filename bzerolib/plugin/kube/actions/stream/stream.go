package stream

type StreamSubAction string

const (
	StreamStart StreamSubAction = "kube/stream/start"
	StreamStop  StreamSubAction = "kube/stream/datain"
)
