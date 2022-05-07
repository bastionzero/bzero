package portforward

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/tests"
	"golang.org/x/build/kubernetes/api"
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
	RunSpecs(t, "Daemon PortForward suite")
}

var _ = Describe("Daemon PortForward action", Ordered, func() {
	oldGetUpgradedConnection := getUpgradedConnection
	oldPerformHandshake := performHandshake

	AfterAll(func() {
		getUpgradedConnection = oldGetUpgradedConnection
		performHandshake = oldPerformHandshake
	})

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

		doneChan := make(chan struct{})
		outputChan := make(chan plugin.ActionWrapper, 10)

		headers := http.Header{}
		headers.Set(kubeutils.PortForwardRequestIDHeader, requestId)

		request := tests.MockHttpRequest("GET", urlPath, headers, sendData)

		mockStreamConnection := tests.MockStreamConnection{}
		mockStreamConnection.On("SetIdleTimeout", kubeutils.DefaultIdleTimeout).Return()
		closeChan := make(chan bool)
		mockStreamConnection.On("CloseChan").Return(closeChan)
		mockStreamConnection.On("Close").Return(nil)

		writer := tests.MockResponseWriter{}

		setPerformHandshake()
		dataHeaders, errorHeaders := setGetUpgradedConnection(&mockStreamConnection)

		mockDataStream := tests.MockStream{MyStreamData: streamData}
		mockDataStream.On("Headers").Return(dataHeaders)
		mockDataStream.On("Close").Return(nil)
		mockDataStream.On("Read").Return(len(streamData), nil)
		mockDataStream.On("Write", []byte(testData)).Return(len(testData), nil)

		mockErrorStream := tests.MockStream{MyStreamData: streamError}
		mockErrorStream.On("Headers").Return(errorHeaders)
		mockErrorStream.On("Close").Return(nil)
		mockErrorStream.On("Read").Return(len(streamError), nil)
		mockErrorStream.On("Write", []byte(testData)).Return(len(testData), nil)

		mockStreamConnection.On("CreateStream", dataHeaders).Return(mockDataStream, nil)
		mockStreamConnection.On("CreateStream", errorHeaders).Return(mockErrorStream, nil)

		p := New(logger, outputChan, doneChan, requestId, logId, command)

		// NOTE: we can't make extensive use of the hierarchy here because we're evaluating messages being passed as state changes
		It("handles the portforwarding session correctly", func() {
			go func() {
				startMessage := <-outputChan

				By("sending a PortForward pyaload to the agent")
				Expect(startMessage.Action).To(Equal(string(portforward.StartPortForward)))
				var payload portforward.KubePortForwardStartActionPayload
				err := json.Unmarshal(startMessage.ActionPayload, &payload)
				Expect(err).To(BeNil())

				p.ReceiveStream(smsg.StreamMessage{
					Type:    smsg.Ready,
					Content: "",
				})

				By("registering incoming data and error streams")
				// these will come in any order and as many times as we ask for them
				n := 0
				receivedDataIn := false
				receivedError := false
				// technically this is nondeterministic, but it'll only fail one in a million times
				for n < 20 {
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

				By("ending when it is killed")
				go p.Kill()

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

				By("processing data and erorr output in the correct order")
				// confirm the above assumptions here
				writer.AssertExpectations(GinkgoT())
			}()

			By("starting without error")
			err := p.Start(&writer, &request)
			Expect(err).To(BeNil())
		})

	})

	Context("Agent-side error", func() {

		doneChan := make(chan struct{})
		outputChan := make(chan plugin.ActionWrapper, 1)

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

		p := New(logger, outputChan, doneChan, requestId, logId, command)

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
				By("returning the error to the user")
				time.Sleep(time.Second)
				writer.AssertExpectations(GinkgoT())
			}()

			By("returning an error to the datachannel")
			err := p.Start(&writer, &request)
			Expect(err).To(Equal(fmt.Errorf("error starting portforward stream: %s", errorStr)))
		})
	})
})
