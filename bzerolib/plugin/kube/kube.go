package kube

type KubeAction string

const (
	Exec        KubeAction = "exec"
	Stream      KubeAction = "stream"
	RestApi     KubeAction = "restapi"
	PortForward KubeAction = "portforward"
)

type KubeActionParams struct {
	TargetUser   string   `json:"targetUser"`
	TargetGroups []string `json:"targetGroups"`
}
