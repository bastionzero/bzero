package stream

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/stream"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/tests"
	"gopkg.in/tomb.v2"
)

func TestSteam(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Books Suite")
}

var _ = Describe("Daemon Stream action", func() {
	var tmb tomb.Tomb
	logger := logger.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "logs"
	sendData := "send data"
	receiveData1 := "receive data"
	receiveData2 := "receive data 2"
	receiveData4 := "receive data 4"
	urlPath := "test-path"

	Context("Happy path", func() {
		request := tests.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)
		writer := tests.MockResponseWriter{}
		writer.On("Write", []byte(receiveData1)).Return(12, nil)
		writer.On("Write", []byte(receiveData2)).Return(14, nil)
		writer.On("Header").Return(make(map[string][]string))

		s, outputChan := New(logger, requestId, logId, command)

		// NOTE: because Start() doesn't return until its work is done, we can't make extensive use of the DSL
		It("sends the correct Stream payload to the agent, processes streams in the correct order, ends, and returns without error", func() {
			go func() {
				startMessage := <-outputChan
				Expect(startMessage.Action).To(Equal(string(stream.StreamStart)))

				// payload should contain the user's request
				var payload stream.KubeStreamActionPayload
				err := json.Unmarshal(startMessage.ActionPayload, &payload)
				Expect(err).To(BeNil())
				Expect(payload.Body).To(Equal(sendData))

				// send early streams (msgs 1 and 4)
				message1 := smsg.StreamMessage{
					Type:           smsg.Data,
					SequenceNumber: 1,
					More:           true,
					Content:        base64.StdEncoding.EncodeToString([]byte(receiveData1)),
				}
				s.ReceiveStream(message1)
				message4 := smsg.StreamMessage{
					Type:           smsg.Data,
					SequenceNumber: 4,
					More:           true,
					Content:        base64.StdEncoding.EncodeToString([]byte(receiveData4)),
				}

				// this functions as an implicit assertion because there is no On() call accounting for it
				// so if the message were processed, we'd panic
				s.ReceiveStream(message4)

				// send header message
				kubeWatchHeadersPayload := stream.KubeStreamHeadersPayload{
					Headers: map[string][]string{
						"Content-Length": {"12"},
						"Origin":         {"val1", "val2"},
					},
				}
				kubeWatchHeadersPayloadBytes, _ := json.Marshal(kubeWatchHeadersPayload)

				message0 := smsg.StreamMessage{
					Type:           smsg.Data,
					SequenceNumber: 0,
					Content:        base64.StdEncoding.EncodeToString(kubeWatchHeadersPayloadBytes),
				}
				// upon receiving this, message 1 will also be processed
				s.ReceiveStream(message0)

				message2 := smsg.StreamMessage{
					Type:           smsg.Data,
					SequenceNumber: 2,
					More:           true,
					Content:        base64.StdEncoding.EncodeToString([]byte(receiveData2)),
				}

				s.ReceiveStream(message2)
				// send this again -- still shouldn't be processed
				s.ReceiveStream(message4)

				// send a stream end message
				endMessage := smsg.StreamMessage{
					Type:           smsg.Data,
					SequenceNumber: 5,
					More:           false,
					Content:        base64.StdEncoding.EncodeToString([]byte("the end")),
				}
				s.ReceiveStream(endMessage)

				// confirm the above assumptions here
				time.Sleep(1 * time.Second)
				writer.AssertExpectations(GinkgoT())
			}()

			err := s.Start(&tmb, &writer, &request)
			Expect(err).To(BeNil())
		})
	})
})

// could have a test that ends via Context().Done() or a tomb kill
// but both of those also return a nil error
