package plugin

// Plugins this datachannel accepts
type PluginName string

const (
	Kube  PluginName = "kube"
	Db    PluginName = "db"
	Web   PluginName = "web"
	Shell PluginName = "shell"
)

type IPlugin interface {
	Receive(action string, actionPayload []byte) (string, []byte, error)
	// Check() bool --> a function that verifies that the plugin can be run in the current environment
}
