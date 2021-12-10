package plugin

type IPlugin interface {
	Receive(action string, actionPayload []byte) (string, []byte, error)
}
