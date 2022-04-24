package plugin

type ActionWrapper struct {
	Action        string
	ActionPayload interface{}
}

type PluginName string

const (
	Kube  PluginName = "kube"
	Db    PluginName = "db"
	Web   PluginName = "web"
	Shell PluginName = "shell"
)
