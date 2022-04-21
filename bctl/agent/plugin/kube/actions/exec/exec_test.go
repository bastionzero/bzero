package exec

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"testing"

	"bastionzero.com/bctl/v1/bzerolib/mocks"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	execaction "bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/exec"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"gopkg.in/tomb.v2"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

func buildStartActionPayload(headers map[string][]string, requestId string, version smsg.SchemaVersion) execaction.KubeExecStartActionPayload {
	return execaction.KubeExecStartActionPayload{
		Endpoint:             "test/endpoint",
		RequestId:            requestId,
		StreamMessageVersion: version,
		LogId:                "lid",
		IsTty:                true,
		Command:              []string{"command"},
		CommandBeingRun:      "command",
	}
}

func buildStdinActionPayload(requestId string, data []byte) execaction.KubeStdinActionPayload {
	return execaction.KubeStdinActionPayload{
		RequestId: requestId,
		LogId:     "lid",
		Stdin:     data,
	}
}

type MockExecutor struct {
	mock.Mock
	remotecommand.Executor
}

func (m MockExecutor) Stream(options remotecommand.StreamOptions) error {
	var data = make([]byte, 7)
	go func() {
		for {
			options.Stdin.Read(data)
			options.Stdout.Write(data)
			options.Stderr.Write([]byte(fmt.Sprintf("error: %s", data)))
		}
	}()

	// NOTE: using Called() with the entire options object is infeasible
	// because the action creates some of its own pointers
	// it's enough to check that stdwriter has the right members
	args := m.Called(options.Stdout)
	return args.Error(0)
}

func setGetExecutor(mockExec MockExecutor) {
	getExecutor = func(config *rest.Config, method string, url *url.URL) (remotecommand.Executor, error) {
		return mockExec, nil
	}
}

func setGetConfig() {
	getConfig = func() (*rest.Config, error) {
		return &rest.Config{}, nil
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	oldGetExecutor := getExecutor
	oldGetConfig := getConfig
	defer func() {
		getExecutor = oldGetExecutor
		getConfig = oldGetConfig
	}()

	exitCode := m.Run()

	// Exit
	os.Exit(exitCode)
}

func TestExec(t *testing.T) {
	assert := assert.New(t)
	logger := mocks.MockLogger()
	var tmb tomb.Tomb
	outputChan := make(chan smsg.StreamMessage, 5)

	requestId := "rid"
	logId := "lid"
	testString := "echo hi"

	mockExec := MockExecutor{}
	stdoutWriter := NewStdWriter(outputChan, smsg.CurrentSchema, requestId, string(kube.Exec), smsg.StdOut, logId)
	mockExec.On("Stream", stdoutWriter).Return(nil)
	setGetExecutor(mockExec)
	setGetConfig()

	e, err := New(logger, &tmb, "serviceAccountToken", "kubeHost", make([]string, 0), "test user", outputChan)
	assert.Nil(err)

	// test start
	startPayload := buildStartActionPayload(make(map[string][]string), requestId, smsg.CurrentSchema)
	startPayloadBytes, err := json.Marshal(startPayload)
	assert.Nil(err)

	action, responsePayload, err := e.Receive(string(execaction.ExecStart), startPayloadBytes)
	assert.Nil(err)
	assert.Equal(string(execaction.ExecStart), action)
	assert.Equal([]byte{}, responsePayload)

	readyMessage := <-outputChan
	readyContent, err := base64.StdEncoding.DecodeString(readyMessage.Content)
	assert.Nil(err)
	assert.Equal([]byte(execaction.EscChar), readyContent)

	// test stdin/stdout
	stdinPayload := buildStdinActionPayload(requestId, []byte(testString))
	stdinPayloadBytes, err := json.Marshal(stdinPayload)
	assert.Nil(err)

	action, responsePayload, err = e.Receive(string(execaction.ExecInput), stdinPayloadBytes)
	assert.Nil(err)
	assert.Equal(string(execaction.ExecInput), action)
	assert.Equal([]byte{}, responsePayload)

	mocks.AssertNextMessageHasContent(assert, outputChan, testString)
	mocks.AssertNextMessageHasContent(assert, outputChan, fmt.Sprintf("error: %s", testString))

	mockExec.AssertExpectations(t)
}
