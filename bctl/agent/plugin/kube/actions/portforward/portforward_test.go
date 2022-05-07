package portforward

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/tests"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
)

// what portforward action will receive from "bastion"
func buildStartActionPayload(headers map[string][]string, requestId string, version smsg.SchemaVersion) []byte {
	payloadBytes, _ := json.Marshal(portforward.KubePortForwardStartActionPayload{
		Endpoint:             "test/endpoint",
		DataHeaders:          make(map[string]string),
		ErrorHeaders:         make(map[string]string),
		RequestId:            requestId,
		StreamMessageVersion: version,
		LogId:                "lid",
		CommandBeingRun:      "command",
	})
	return payloadBytes
}

// what portforward action will receive from "bastion"
func buildActionPayload(bodyText string, requestId string, portForwardRequestId string) []byte {
	payloadBytes, _ := json.Marshal(portforward.KubePortForwardActionPayload{
		RequestId:            requestId,
		LogId:                "lid",
		Data:                 []byte(bodyText),
		PortForwardRequestId: portForwardRequestId,
		PodPort:              5000,
	})
	return payloadBytes
}

// what portforward action will receive from "bastion"
func buildStopRequestActionPayload(requestId string, portForwardRequestId string) []byte {
	payloadBytes, _ := json.Marshal(portforward.KubePortForwardStopRequestActionPayload{
		RequestId:            requestId,
		LogId:                "lid",
		PortForwardRequestId: portForwardRequestId,
	})
	return payloadBytes
}

// what portforward action will receive from "bastion"
func buildStopActionPayload(requestId string) []byte {
	payloadBytes, _ := json.Marshal(portforward.KubePortForwardStopActionPayload{
		RequestId: requestId,
		LogId:     "lid",
	})
	return payloadBytes
}

// inject our mocked object
func setDoDial(streamConnection *tests.MockStreamConnection) {
	doDial = func(dialer httpstream.Dialer, protocolName string) (httpstream.Connection, string, error) {
		return streamConnection, "", nil
	}
}

// save portforward action the trouble of trying to read a nonexsitent config
func setGetConfig() {
	getConfig = func() (*rest.Config, error) {
		return &rest.Config{}, nil
	}
}

func TestPortForward(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Portforward Suite")
}

var _ = Describe("Agent PortForward action", Ordered, func() {
	oldDoDial := doDial
	AfterAll(func() {
		doDial = oldDoDial
	})

	logger := logger.MockLogger()

	requestId := "rid"
	portForwardRequestId := "pid"
	testData := "test data"

	Context("Happy path", func() {
		outputChan := make(chan smsg.StreamMessage, 30)
		doneChan := make(chan struct{})
		mockStream := tests.MockStream{MyStreamData: testData}
		mockStream.On("Read").Return(len(testData), nil)
		mockStream.On("Write", []byte(testData)).Return(len(testData), nil)
		mockStream.On("Close").Return(nil)

		mockStreamConnection := new(tests.MockStreamConnection)
		mockStreamConnection.On("CreateStream", http.Header{
			"Port":      []string{"5000"},
			"Requestid": []string{portForwardRequestId},
		}).Return(mockStream, nil)
		mockStreamConnection.On("Close").Return(nil)
		closeChan := make(chan bool)
		mockStreamConnection.On("CloseChan").Return(closeChan)

		setDoDial(mockStreamConnection)
		setGetConfig()

		p := New(logger, outputChan, doneChan, "serviceAccountToken", "kubeHost", make([]string, 0), "test user")

		It("handles the portforwarding session correctly", func() {
			By("receiving a PortForward request without error")
			payload := buildStartActionPayload(make(map[string][]string), requestId, smsg.CurrentSchema)
			responsePayload, err := p.Receive(string(portforward.StartPortForward), payload)
			Expect(err).To(BeNil())
			Expect(responsePayload).To(Equal([]byte{}))

			readyMessage := <-outputChan
			By("alerting that it has started the portforward interaction with the kube server")
			Expect(readyMessage.Content).To(Equal(""))

			payload = buildActionPayload(testData, requestId, portForwardRequestId)
			By("receiving data from the remote port")
			responsePayload, err = p.Receive(string(portforward.DataInPortForward), payload)
			Expect(err).To(BeNil())
			Expect(responsePayload).To(Equal([]byte{}))

			dataMessage := <-outputChan

			By("forwarding data to the daemon")
			wrappedContent, _ := base64.StdEncoding.DecodeString(dataMessage.Content)
			var content portforward.KubePortForwardStreamMessageContent
			json.Unmarshal(wrappedContent, &content)
			Expect(string(content.Content)).To(Equal(testData))

			By("ending a particular portforward request when the daemon sends a stop request message")
			stopRequestPayload := buildStopRequestActionPayload(requestId, portForwardRequestId)
			responsePayload, err = p.Receive(string(portforward.StopPortForwardRequest), stopRequestPayload)
			Expect(err).To(BeNil())
			Expect(responsePayload).To(Equal([]byte{}))

			By("closing when the daemon sends a stop message")
			stopPayload := buildStopActionPayload(requestId)
			responsePayload, err = p.Receive(string(portforward.StopPortForward), stopPayload)
			close(closeChan)
			Expect(err).To(BeNil())
			Expect(responsePayload).To(Equal([]byte{}))

			By("interacting with the stream connection as expected")
			time.Sleep(time.Second)
			mockStream.AssertExpectations(GinkgoT())
			mockStreamConnection.AssertExpectations(GinkgoT())
		})
	})
})
