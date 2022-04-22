package stream

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"testing"

	"bastionzero.com/bctl/v1/bzerolib/mocks"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/stream"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"github.com/stretchr/testify/assert"
	"gopkg.in/tomb.v2"
)

// what stream action will receive from "bastion"
func buildActionPayload(headers map[string][]string, requestId string, version smsg.SchemaVersion) []byte {
	payloadBytes, _ := json.Marshal(stream.KubeStreamActionPayload{
		Endpoint:             "test/endpoint",
		Headers:              headers,
		Method:               "GET",
		Body:                 "",
		RequestId:            requestId,
		StreamMessageVersion: version,
		LogId:                "lid",
		CommandBeingRun:      "command",
	})
	return payloadBytes
}

// inject logic for what happens when stream makes an HTTP request
func setMakeRequest(statusCode int, headers map[string][]string, bodyText string) {
	makeRequest = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: statusCode,
			Header:     headers,
			Body:       ioutil.NopCloser(bytes.NewBufferString(bodyText)), //http.bodyEOFSignal{body: 0xc0003b6b40, mu: {state: 0, sema: 0}, closed: false, rerr: nil, fn: 0x70d480, earlyCloseFn: 0x70d400},
		}, nil
	}
}

func TestMain(m *testing.M) {
	flag.Parse()

	exitCode := m.Run()

	// Exit
	os.Exit(exitCode)
}

func TestStream(t *testing.T) {
	assert := assert.New(t)
	logger := mocks.MockLogger()
	var tmb tomb.Tomb
	outputChan := make(chan smsg.StreamMessage, 10)

	requestId := "rid"
	testString := "test"
	headers := map[string][]string{
		"Audit-Id":      {"value1"},
		"Cache-Control": {"value2"},
	}

	// resopnd with a 4kB string
	setMakeRequest(200, headers, strings.Repeat(testString, 1024))

	t.Logf("Test that we can create a new Stream action")
	s, err := New(logger, &tmb, "serviceAccountToken", "kubeHost", make([]string, 0), "test user", outputChan)
	assert.Nil(err)

	payload := buildActionPayload(make(map[string][]string), requestId, smsg.CurrentSchema)
	t.Logf("Test that we can ask the action to start streaming")
	action, responsePayload, err := s.Receive(string(stream.StreamStart), payload)
	assert.Nil(err)
	assert.Equal(string(stream.StreamStart), action)
	assert.Equal([]byte{}, responsePayload)

	// read the header message
	headerMessage := <-outputChan

	var kubestreamHeadersPayload stream.KubeStreamHeadersPayload
	contentBytes, err := base64.StdEncoding.DecodeString(headerMessage.Content)
	assert.Nil(err)

	t.Logf("Test that the action first returns a message with the stream's headers")
	err = json.Unmarshal(contentBytes, &kubestreamHeadersPayload)
	assert.Nil(err)
	assert.Equal(requestId, headerMessage.RequestId)
	assert.Equal(stream.KubeStreamHeadersPayload{
		Headers: headers,
	}, kubestreamHeadersPayload)

	t.Logf("Test that the 4kb of content that were streamed are returned in 1kb chunks")
	for n := 0; n < 4; n++ {
		bodyMessage := <-outputChan
		contentBytes, err = base64.StdEncoding.DecodeString(bodyMessage.Content)
		assert.Nil(err)
		// expect 1024 bytes per message
		assert.Equal(strings.Repeat(testString, 256), string(contentBytes))
		assert.Equal(true, bodyMessage.More)
	}

	finalMessage := <-outputChan
	t.Logf("Test that the action informs us that the stream has ended")
	assert.Equal(false, finalMessage.More)
}
