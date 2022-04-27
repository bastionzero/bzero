package tests

import (
	"encoding/base64"

	"github.com/onsi/gomega"

	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// assert that the content of the stream message coming from outputChan is equal to testSTring
// this is a common pattern when testing the agent
func ExpectNextMessageHasContent(outputChan chan smsg.StreamMessage, testString string) {
	message := <-outputChan
	content, err := base64.StdEncoding.DecodeString(message.Content)
	gomega.Expect(err).To(gomega.BeNil())
	gomega.Expect(content).To(gomega.Equal([]byte(testString)))
}
