package stream

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/stream"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
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

func TestStream(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Stream Suite")
}

var _ = Describe("Agent Stream action", Ordered, func() {
	logger := logger.MockLogger()

	requestId := "rid"
	testString := "test"
	headers := map[string][]string{
		"Audit-Id":      {"value1"},
		"Cache-Control": {"value2"},
	}

	Context("Happy path", func() {
		doneChan := make(chan struct{})
		outputChan := make(chan smsg.StreamMessage, 10)
		// respond with a 4kb string
		setMakeRequest(200, headers, strings.Repeat(testString, 1024))
		s := New(logger, outputChan, doneChan, "serviceAccountToken", "kubeHost", make([]string, 0), "test user")

		It("streams a 4kb message in chunks", func() {
			By("receiving a stream request without error")
			payload := buildActionPayload(make(map[string][]string), requestId, smsg.CurrentSchema)
			responsePayload, err := s.Receive(string(stream.StreamStart), payload)
			Expect(err).To(BeNil())
			Expect(responsePayload).To(Equal([]byte{}))

			By("first returning a message with the stream's headers")
			headerMessage := <-outputChan

			var kubestreamHeadersPayload stream.KubeStreamHeadersPayload
			contentBytes, _ := base64.StdEncoding.DecodeString(headerMessage.Content)
			json.Unmarshal(contentBytes, &kubestreamHeadersPayload)

			Expect(kubestreamHeadersPayload).To(Equal(stream.KubeStreamHeadersPayload{
				Headers: headers,
			}))

			By("breaking up the stream into 1kb chunks")
			for n := 0; n < 4; n++ {
				bodyMessage := <-outputChan
				contentBytes, err = base64.StdEncoding.DecodeString(bodyMessage.Content)
				Expect(err).To(BeNil())
				// expect 1024 bytes per message
				Expect(string(contentBytes)).To(Equal(strings.Repeat(testString, 256)))
				Expect(bodyMessage.More).To(BeTrue())
			}

			finalMessage := <-outputChan
			By("informing the datachannel that the stream has ended")
			Expect(finalMessage.More).To(BeFalse())
		})
	})
})
