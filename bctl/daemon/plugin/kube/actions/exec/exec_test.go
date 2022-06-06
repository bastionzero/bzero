package exec

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/exec"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/tests"
)

// inject our mock object
func setNewSPDYService(mockSpdy *SPDYService) {
	NewSPDYService = func(logger *logger.Logger, writer http.ResponseWriter, request *http.Request) (*SPDYService, error) {
		return mockSpdy, nil
	}
}

func TestExec(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Daemon Exec suite")
}

var _ = Describe("Daemon Exec action", Ordered, func() {
	oldNewSPDYService := NewSPDYService

	AfterAll(func() {
		NewSPDYService = oldNewSPDYService
	})

	logger := logger.MockLogger()

	requestId := "rid"
	logId := "lid"
	command := "exec"
	sendData := "send data"
	receiveData := "receive data"
	streamData := "stream data"
	urlPath := "test-path"

	mockStdinStream := tests.MockStream{MyStreamData: streamData}
	mockStdinStream.On("Read").Return(len(streamData), nil)

	mockStdoutStream := tests.MockStream{}
	mockStdoutStream.On("Write", []byte(receiveData)).Return(len(receiveData), nil)

	mockStderrStream := tests.MockStream{}
	mockResizeStream := tests.MockStream{}
	mockStreamConnection := new(tests.MockStreamConnection)

	closeChan := make(chan bool)
	mockStreamConnection.On("CloseChan").Return(closeChan)
	mockStreamConnection.On("Close").Return(nil)

	mockSpdy := &SPDYService{
		logger:       logger,
		stdinStream:  mockStdinStream,
		stdoutStream: mockStdoutStream,
		stderrStream: mockStderrStream,
		resizeStream: mockResizeStream,
		conn:         mockStreamConnection,
	}

	setNewSPDYService(mockSpdy)

	request := tests.MockHttpRequest("GET", urlPath, map[string][]string{"X-Stream-Protocol-Version": {"test"}}, sendData)

	writer := tests.MockResponseWriter{}

	Context("Happy path", func() {
		doneChan := make(chan struct{})
		outputChan := make(chan plugin.ActionWrapper, 1)
		e := New(logger, outputChan, doneChan, requestId, logId, command)

		// NOTE: we can't make extensive use of the hierarchy here because we're evaluating messages being passed as state changes
		It("handles the exec session correctly", func() {
			By("starting without error")
			err := e.Start(&writer, &request)
			Expect(err).To(BeNil())

			startMessage := <-outputChan

			By("sending an Exec payload to the agent")
			Expect(startMessage.Action).To(Equal(string(exec.ExecStart)))
			var payload exec.KubeExecStartActionPayload
			err = json.Unmarshal(startMessage.ActionPayload, &payload)
			Expect(err).To(BeNil())

			message0 := smsg.StreamMessage{
				SequenceNumber: 0,
				Content:        base64.StdEncoding.EncodeToString([]byte(receiveData)),
			}
			e.ReceiveStream(message0)

			messageEnd := smsg.StreamMessage{
				SequenceNumber: 1,
				Content:        base64.StdEncoding.EncodeToString([]byte(exec.EscChar)),
			}
			e.ReceiveStream(messageEnd)

			// give it time to finish
			time.Sleep(time.Second)

			By("by writing stdout data received from the agent to the user")
			writer.AssertExpectations(GinkgoT())
		})
	})
})
