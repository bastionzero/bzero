package exec

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"testing"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	bzexec "bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/exec"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/tests"
	"github.com/stretchr/testify/assert"
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
	logger := logger.MockLogger()
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

	t.Logf("Test that we can create a new Exec action")
	e, err := New(logger, &tmb, "serviceAccountToken", "kubeHost", make([]string, 0), "test user", outputChan)
	assert.Nil(err)

	// test start
	startPayloadBytes := buildStartActionPayload(make(map[string][]string), requestId, smsg.CurrentSchema)

	t.Logf("Test that we can initiate an exec session")
	action, responsePayload, err := e.Receive(string(bzexec.ExecStart), startPayloadBytes)
	assert.Nil(err)
	assert.Equal(string(bzexec.ExecStart), action)
	assert.Equal([]byte{}, responsePayload)

	readyMessage := <-outputChan
	readyContent, err := base64.StdEncoding.DecodeString(readyMessage.Content)
	t.Logf("Test that the action has started the exec interaction with the kube server")
	assert.Nil(err)
	assert.Equal([]byte(bzexec.EscChar), readyContent)

	// test stdin/stdout
	stdinPayloadBytes := buildStdinActionPayload(requestId, []byte(testString))

	action, responsePayload, err = e.Receive(string(bzexec.ExecInput), stdinPayloadBytes)
	assert.Nil(err)
	t.Logf("Test that the action can accept input from stdin")
	assert.Equal(string(bzexec.ExecInput), action)
	assert.Equal([]byte{}, responsePayload)

	t.Logf("Test that the action reports output from stdout and stderr")
	tests.AssertNextMessageHasContent(assert, outputChan, testString)
	tests.AssertNextMessageHasContent(assert, outputChan, fmt.Sprintf("error: %s", testString))

	t.Logf("Test that the action is closed")
	assert.True(e.Closed())

	mockExec.AssertExpectations(t)
}
