package portforward

const (
	DataStreamBufferSize  = 1024 * 1024
	ErrorStreamBufferSize = 1024 * 1024
)

// Portforward payload for the "kube/portforward/start" action
type KubePortForwardStartActionPayload struct {
	RequestId       string            `json:"requestId"`
	LogId           string            `json:"logId"`
	Endpoint        string            `json:"endpoint"`
	DataHeaders     map[string]string `json:"dataHeaders"`
	ErrorHeaders    map[string]string `json:"errorHeaders"`
	CommandBeingRun string            `json:"commandBeingRun"`
}

// Portforward payload for the "kube/portforward/datain" action
type KubePortForwardActionPayload struct {
	RequestId            string `json:"requestId"`
	LogId                string `json:"logId"`
	Data                 []byte `json:"data"`
	PortForwardRequestId string `json:"portForwardRequestId"`
	PodPort              int64  `json:"podPort"`
}

// Portforward payload for the "kube/portforward/request/sop" action
type KubePortForwardStopRequestActionPayload struct {
	RequestId            string `json:"requestId"`
	LogId                string `json:"logId"`
	PortForwardRequestId string `json:"portForwardRequestId"`
}

// Portforward payload for the "kube/portforward/stop" action
type KubePortForwardStopActionPayload struct {
	RequestId string `json:"requestId"`
	LogId     string `json:"logId"`
}

// Portforward payload for stream messages
type KubePortForwardStreamMessageContent struct {
	PortForwardRequestId string `json:"portForwardRequestId"`
	Content              []byte `json:"content"`
}
