package exec

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/mocks"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/exec"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"github.com/stretchr/testify/assert"
	"gopkg.in/tomb.v2"
)

func setNewSPDYService(mockSpdy *SPDYService) {
	NewSPDYService = func(logger *logger.Logger, writer http.ResponseWriter, request *http.Request) (*SPDYService, error) {
		return mockSpdy, nil
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	oldNewSPDYService := NewSPDYService
	defer func() { NewSPDYService = oldNewSPDYService }()

	exitCode := m.Run()

	// Exit
	os.Exit(exitCode)
}

func TestExec(t *testing.T) {
	assert := assert.New(t)
	var tmb tomb.Tomb
	logger := mocks.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "exec"
	sendData := "send data"
	receiveData := "receive data"
	streamData := "stream data"
	urlPath := "test-path"
	e, outputChan := New(logger, requestId, logId, command)

	mockStdinStream := mocks.MockStream{MyStreamData: streamData}
	mockStdinStream.On("Read", make([]byte, kubeutils.ExecChunkSize)).Return(len(streamData), nil)

	mockStdoutStream := mocks.MockStream{}
	mockStdoutStream.On("Write", []byte(receiveData)).Return(len(receiveData), nil)

	mockStderrStream := mocks.MockStream{}
	mockResizeStream := mocks.MockStream{}
	mockStreamConnection := new(mocks.MockStreamConnection)

	var closeChan <-chan bool

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

	request := mocks.MockHttpRequest("GET", urlPath, map[string][]string{"X-Stream-Protocol-Version": {"test"}}, sendData)

	writer := mocks.MockResponseWriter{}

	err := e.Start(&tmb, &writer, &request)
	assert.Nil(err)
	reqMessage := <-outputChan

	assert.Equal(string(exec.ExecStart), reqMessage.Action)
	var payload exec.KubeExecStartActionPayload
	err = json.Unmarshal(reqMessage.ActionPayload, &payload)
	assert.Nil(err)
	assert.Equal(command, payload.CommandBeingRun)
	assert.Equal(requestId, payload.RequestId)
	assert.Equal(logId, payload.LogId)

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

	// FIXME: race, need to give the code time to run
	time.Sleep(time.Second)

	writer.AssertExpectations(t)

}
