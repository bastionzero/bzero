package testutils

import (
	"bytes"
	"encoding/base64"
	"io/ioutil"
	"net/http"
	"net/url"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"github.com/stretchr/testify/assert"

	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

func MockLogger() *logger.Logger {
	logger, err := logger.New(logger.DefaultLoggerConfig(logger.Debug.String()), "/dev/null")
	if err == nil {
		return logger
	}
	return nil
}

func B64Encode(b []byte) []byte {
	// Adds quotes as the base64 encoded strings which receive gets over the data channel has quotes
	return []byte("\"" + base64.StdEncoding.EncodeToString(b) + "\"")
}

// create an http request with the specified details
func MockHttpRequest(method string, path string, headers map[string][]string, content string) http.Request {
	return http.Request{
		Method:        method,
		URL:           &url.URL{Path: path},
		Header:        headers,
		ContentLength: int64(len(content)),
		Body:          ioutil.NopCloser(bytes.NewBufferString(content)),
	}
}

// assert that the content of the stream message coming from outputChan is equal to testSTring
// this is a common pattern when testing the agent
func AssertNextMessageHasContent(assert *assert.Assertions, outputChan chan smsg.StreamMessage, testString string) {
	message := <-outputChan
	content, err := base64.StdEncoding.DecodeString(message.Content)
	assert.Nil(err)
	assert.Equal([]byte(testString), content)
}
