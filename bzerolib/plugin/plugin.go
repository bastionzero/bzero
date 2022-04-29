package plugin

type ActionWrapper struct {
	Action        string
	ActionPayload []byte
}

type PluginName string

const (
	Db    PluginName = "db"
	Kube  PluginName = "kube"
	Shell PluginName = "shell"
	Ssh   PluginName = "ssh"
	Web   PluginName = "web"
)
