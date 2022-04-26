package portforward

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/tests"
	"golang.org/x/build/kubernetes/api"
	"gopkg.in/tomb.v2"
	"k8s.io/apimachinery/pkg/util/httpstream"
)

// let the action call a dummy handshake
func setPerformHandshake() {
	performHandshake = func(req *http.Request, w http.ResponseWriter, serverProtocols []string) (string, error) {
		return "", nil
	}
}

// inject a mock stream pair that sends data / error messages so that portforward can establish a complete connection
func setGetUpgradedConnection(mockConnection *tests.MockStreamConnection) (http.Header, http.Header) {
	dataHeaders := http.Header{}
	dataHeaders.Set(kubeutils.StreamType, kubeutils.StreamTypeData)
	errorHeaders := http.Header{}
	errorHeaders.Set(kubeutils.StreamType, kubeutils.StreamTypeError)
	getUpgradedConnection = func(w http.ResponseWriter, req *http.Request, streamChan chan httpstream.Stream, pingPeriod time.Duration) httpstream.Connection {
		dataHeaders.Set(kubeutils.PortForwardRequestIDHeader, req.Header.Get(kubeutils.PortForwardRequestIDHeader))
		errorHeaders.Set(kubeutils.PortForwardRequestIDHeader, req.Header.Get(kubeutils.PortForwardRequestIDHeader))
		go func() {
			dataStream, _ := mockConnection.CreateStream(dataHeaders)
			errorStream, _ := mockConnection.CreateStream(errorHeaders)
			streamChan <- dataStream
			streamChan <- errorStream
		}()
		return mockConnection
	}
	return dataHeaders, errorHeaders
}

func TestPortForward(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Books Suite")
}

var _ = Describe("Daemon PortForward action", Ordered, func() {
	oldGetUpgradedConnection := getUpgradedConnection
	oldPerformHandshake := performHandshake

	AfterAll(func() {
		getUpgradedConnection = oldGetUpgradedConnection
		performHandshake = oldPerformHandshake
	})

	var tmb tomb.Tomb
	logger := logger.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "logs"
	sendData := "send data"
	urlPath := "test-path"
	errorStr := "test error"
	testData := "receive data"
	streamData := "stream data"
	streamError := "stream error"

	request := tests.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)

	Context("Happy path", func() {

		headers := http.Header{}
		headers.Set(kubeutils.PortForwardRequestIDHeader, requestId)

		request := tests.MockHttpRequest("GET", urlPath, headers, sendData)

		mockStreamConnection := tests.MockStreamConnection{}
		mockStreamConnection.On("SetIdleTimeout", kubeutils.DefaultIdleTimeout).Return()
		var closeChan <-chan bool
		mockStreamConnection.On("CloseChan").Return(closeChan)
		mockStreamConnection.On("Close").Return(nil)

		writer := tests.MockResponseWriter{}

		setPerformHandshake()
		dataHeaders, errorHeaders := setGetUpgradedConnection(&mockStreamConnection)

		mockDataStream := tests.MockStream{MyStreamData: streamData}
		mockDataStream.On("Headers").Return(dataHeaders)
		mockDataStream.On("Close").Return(nil)
		mockDataStream.On("Read", make([]byte, portforward.DataStreamBufferSize)).Return(len(streamData), nil)
		mockDataStream.On("Write", []byte(testData)).Return(len(testData), nil).Times(3)

		mockErrorStream := tests.MockStream{MyStreamData: streamError}
		mockErrorStream.On("Headers").Return(errorHeaders)
		mockErrorStream.On("Close").Return(nil)
		mockErrorStream.On("Read", make([]byte, portforward.ErrorStreamBufferSize)).Return(len(streamError), nil)
		mockErrorStream.On("Write", []byte(testData)).Return(len(testData), nil)

		mockStreamConnection.On("CreateStream", dataHeaders).Return(mockDataStream, nil)
		mockStreamConnection.On("CreateStream", errorHeaders).Return(mockErrorStream, nil)

		p, outputChan := New(logger, requestId, logId, command)

		// NOTE: because Start() doesn't return until its work is done, we can't make extensive use of the DSL
		It("sends the correct PortForward payload to the agent, processes portforward messages in the correct order, ends when killed, and returns without error", func() {
			go func() {
				startMessage := <-outputChan

				Expect(startMessage.Action).To(Equal(string(portforward.StartPortForward)))
				var payload portforward.KubePortForwardStartActionPayload
				err := json.Unmarshal(startMessage.ActionPayload, &payload)
				Expect(err).To(BeNil())

				p.ReceiveStream(smsg.StreamMessage{
					Type:    smsg.Ready,
					Content: "",
				})
				// these might come in either order
				n := 0
				receivedDataIn := false
				receivedError := false
				for n < 2 {
					msg := <-outputChan
					switch msg.Action {
					case string(portforward.DataInPortForward):
						var dataInPayload portforward.KubePortForwardActionPayload
						err = json.Unmarshal(msg.ActionPayload, &dataInPayload)
						Expect(err).To(BeNil())
						Expect(dataInPayload.Data).To(Equal([]byte(streamData)))
						receivedDataIn = true
					case string(portforward.ErrorPortForward):
						var errorInPayload portforward.KubePortForwardActionPayload
						err = json.Unmarshal(msg.ActionPayload, &errorInPayload)
						Expect(err).To(BeNil())
						Expect(errorInPayload.Data).To(Equal([]byte(streamError)))
						receivedError = true
					}
					n++
				}
				Expect(receivedDataIn).To(BeTrue())
				Expect(receivedError).To(BeTrue())

				streamMessageToSend := portforward.KubePortForwardStreamMessageContent{
					PortForwardRequestId: requestId,
					Content:              []byte(testData),
				}
				streamMessageToSendBytes, _ := json.Marshal(streamMessageToSend)
				encodedContent := base64.StdEncoding.EncodeToString(streamMessageToSendBytes)

				p.ReceiveStream(smsg.StreamMessage{
					SequenceNumber: 0,
					Content:        encodedContent,
					Type:           smsg.Data,
					More:           true,
				})

				p.ReceiveStream(smsg.StreamMessage{
					SequenceNumber: 2,
					Content:        encodedContent,
					Type:           smsg.Data,
					More:           false,
				})

				p.ReceiveStream(smsg.StreamMessage{
					SequenceNumber: 0,
					Content:        encodedContent,
					Type:           smsg.Error,
					More:           false,
				})

				p.ReceiveStream(smsg.StreamMessage{
					SequenceNumber: 1,
					Content:        encodedContent,
					Type:           smsg.Data,
					More:           true,
				})

				time.Sleep(time.Second)

				// kill the tomb
				tmb.Kill(errors.New("test kill"))

				time.Sleep(time.Second)

				receivedStopRequestMsg := false
				receivedStopMsg := false
				// need to drain the channel, as several extra data and error messages have been sent to it
				for len(outputChan) > 0 {
					msg := <-outputChan
					if msg.Action == string(portforward.StopPortForwardRequest) {
						receivedStopRequestMsg = true
					} else if msg.Action == string(portforward.StopPortForward) {
						receivedStopMsg = true
					}
				}
				Expect(receivedStopRequestMsg).To(BeTrue())
				Expect(receivedStopMsg).To(BeTrue())

				// confirm the above assumptions here
				writer.AssertExpectations(GinkgoT())
			}()

			err := p.Start(&tmb, &writer, &request)
			Expect(err).To(BeNil())
		})

	})

	Context("Agent-side error", func() {
		writer := tests.MockResponseWriter{}
		writer.On("WriteHeader", http.StatusForbidden).Return()
		writer.On("Header").Return(make(map[string][]string))
		// not crazy about this since we have to reuse code from the file we're testing
		errorJson, _ := json.Marshal(api.Status{
			Message: errorStr,
			Status:  api.StatusFailure,
			Code:    http.StatusForbidden,
			Reason:  "Forbidden",
		})

		writer.On("Write", errorJson).Return(len(errorJson), nil)

		p, outputChan := New(logger, requestId, logId, command)

		It("stops if the ready message from the agent contains an error", func() {
			go func() {
				// can ignore this since we're not testing it
				<-outputChan

				p.ReceiveStream(smsg.StreamMessage{
					Type:    smsg.Ready,
					Content: errorStr,
				})

				stopMessage := <-outputChan
				Expect(stopMessage.Action).To(Equal(string(portforward.StopPortForward)))

				// confirm that our mocks were called as expected
				time.Sleep(time.Second)
				writer.AssertExpectations(GinkgoT())
			}()

			err := p.Start(&tmb, &writer, &request)
			Expect(err).To(Equal(fmt.Errorf("error starting portforward stream: %s", errorStr)))
		})
	})
})
