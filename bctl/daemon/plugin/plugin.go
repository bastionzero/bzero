package plugin

import (
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type IPlugin interface {
	ReceiveKeysplitting(action string, actionPayload []byte) (string, []byte, error)
	ReceiveStream(smessage smsg.StreamMessage)
	Feed(food interface{}) error
	// Check() bool --> a function that verifies that the plugin can be run in the current environment
}
