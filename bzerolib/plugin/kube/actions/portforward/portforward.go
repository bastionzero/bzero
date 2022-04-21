package portforward

type PortForwardSubAction string

const (
	StartPortForward       PortForwardSubAction = "kube/portforward/start"
	DataInPortForward      PortForwardSubAction = "kube/portforward/datain"
	ErrorInPortForward     PortForwardSubAction = "kube/portforward/errorin"
	ErrorPortForward       PortForwardSubAction = "kube/portforward/error"
	StopPortForward        PortForwardSubAction = "kube/portforward/stop"
	StopPortForwardRequest PortForwardSubAction = "kube/portforward/request/stop"
)
