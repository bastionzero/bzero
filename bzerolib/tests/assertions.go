package tests

import (
	"encoding/base64"

	"github.com/stretchr/testify/assert"

	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// assert that the content of the stream message coming from outputChan is equal to testSTring
// this is a common pattern when testing the agent
func AssertNextMessageHasContent(assert *assert.Assertions, outputChan chan smsg.StreamMessage, testString string) {
	message := <-outputChan
	content, err := base64.StdEncoding.DecodeString(message.Content)
	assert.Nil(err)
	assert.Equal([]byte(testString), content)
}
