package exec

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	bzexec "bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/exec"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/tests"
	"gopkg.in/tomb.v2"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// what exec action will receive from "bastion"
func buildStartActionPayload(headers map[string][]string, requestId string, version smsg.SchemaVersion) []byte {
	payloadBytes, _ := json.Marshal(bzexec.KubeExecStartActionPayload{
		Endpoint:             "test/endpoint",
		RequestId:            requestId,
		StreamMessageVersion: version,
		LogId:                "lid",
		IsTty:                true,
		Command:              []string{"command"},
		CommandBeingRun:      "command",
	})
	return payloadBytes
}

// what exec will receive from "stdin"
func buildStdinActionPayload(requestId string, data []byte) []byte {
	payloadBytes, _ := json.Marshal(bzexec.KubeStdinActionPayload{
		RequestId: requestId,
		LogId:     "lid",
		Stdin:     data,
	})
	return payloadBytes
}

// inject our mocked object
func setGetExecutor(mockExec MockExecutor) {
	getExecutor = func(config *rest.Config, method string, url *url.URL) (remotecommand.Executor, error) {
		return mockExec, nil
	}
}

// save exec action the trouble of trying to read a nonexsitent config
func setGetConfig() {
	getConfig = func() (*rest.Config, error) {
		return &rest.Config{}, nil
	}
}

func TestExec(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Exec suite")
}

var _ = Describe("Agent Exec action", Ordered, func() {
	oldGetExecutor := getExecutor
	oldGetConfig := getConfig

	AfterAll(func() {
		getExecutor = oldGetExecutor
		getConfig = oldGetConfig
	})

	logger := logger.MockLogger()
	var tmb tomb.Tomb
	outputChan := make(chan smsg.StreamMessage, 5)

	requestId := "rid"
	logId := "lid"
	testString := "echo hi"

	Context("Happy path", func() {
		mockExecutor := MockExecutor{}
		stdoutWriter := NewStdWriter(outputChan, smsg.CurrentSchema, requestId, string(kube.Exec), smsg.StdOut, logId)
		mockExecutor.On("Stream", stdoutWriter).Return(nil)
		setGetExecutor(mockExecutor)
		setGetConfig()
		e, err := New(logger, &tmb, "serviceAccountToken", "kubeHost", make([]string, 0), "test user", outputChan)

		It("handles the exec session correctly", func() {
			By("starting without error")
			Expect(err).To(BeNil())

			startPayloadBytes := buildStartActionPayload(make(map[string][]string), requestId, smsg.CurrentSchema)

			By("receiving an Exec request without error")
			action, responsePayload, err := e.Receive(string(bzexec.ExecStart), startPayloadBytes)
			Expect(err).To(BeNil())
			Expect(action).To(Equal(string(bzexec.ExecStart)))
			Expect(responsePayload).To(Equal([]byte{}))

			readyMessage := <-outputChan
			readyContent, _ := base64.StdEncoding.DecodeString(readyMessage.Content)
			By("by alerting that it has started the exec interaction with the kube server")
			Expect(readyContent).To(Equal([]byte(bzexec.EscChar)))

			By("expecting input from stdin")
			stdinPayloadBytes := buildStdinActionPayload(requestId, []byte(testString))
			action, responsePayload, err = e.Receive(string(bzexec.ExecInput), stdinPayloadBytes)
			Expect(err).To(BeNil())
			Expect(action).To(Equal(string(bzexec.ExecInput)))
			Expect(responsePayload).To(Equal([]byte{}))

			By("reporting output from stdout and stderr")
			tests.ExpectNextMessageHasContent(outputChan, testString)
			tests.ExpectNextMessageHasContent(outputChan, fmt.Sprintf("error: %s", testString))

			By("informing the datachannel it has closed")
			Expect(e.Closed()).To(BeTrue())

			By("writing messages via the stdout writer")
			mockExecutor.AssertExpectations(GinkgoT())
		})
	})
})
