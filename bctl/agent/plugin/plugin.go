package plugin

type IPlugin interface {
	Receive(action string, actionPayload []byte) (string, []byte, error)
	// Check() bool --> a function that verifies that the plugin can be run in the current environment
}
