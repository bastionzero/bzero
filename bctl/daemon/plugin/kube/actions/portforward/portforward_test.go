package portforward

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/testutils"
	"github.com/stretchr/testify/assert"
	"golang.org/x/build/kubernetes/api"
	"gopkg.in/tomb.v2"
	"k8s.io/apimachinery/pkg/util/httpstream"
)

func setPerformHandshake() {
	performHandshake = func(req *http.Request, w http.ResponseWriter, serverProtocols []string) (string, error) {
		return "", nil
	}
}

func setGetUpgradedConnection(mockConnection *testutils.MockStreamConnection) {
	getUpgradedConnection = func(w http.ResponseWriter, req *http.Request, handler httpstream.NewStreamHandler, pingPeriod time.Duration) httpstream.Connection {
		return mockConnection
	}
}

func TestMain(m *testing.M) {
	flag.Parse()

	exitCode := m.Run()

	// Exit
	os.Exit(exitCode)
}

func TestPortForward(t *testing.T) {
	assert := assert.New(t)
	var tmb tomb.Tomb
	logger := testutils.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "logs"
	sendData := "send data"
	urlPath := "test-path"

	request := testutils.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)

	mockStreamConnection := testutils.MockStreamConnection{}
	mockStreamConnection.On("SetIdleTimeout", kubeutils.DefaultIdleTimeout).Return()
	var closeChan <-chan bool
	mockStreamConnection.On("CloseChan").Return(closeChan)
	mockStreamConnection.On("Close").Return(nil)

	writer := testutils.MockResponseWriter{}

	setPerformHandshake()
	setGetUpgradedConnection(&mockStreamConnection)

	p, outputChan := New(logger, requestId, logId, command)

	go func() {
		// get initial payload from output channel
		reqMessage := <-outputChan
		assert.Equal(string(portforward.StartPortForward), reqMessage.Action)
		var payload portforward.KubePortForwardStartActionPayload
		err := json.Unmarshal(reqMessage.ActionPayload, &payload)
		assert.Nil(err)
		assert.Equal(command, payload.CommandBeingRun)
		assert.Equal(requestId, payload.RequestId)
		assert.Equal(logId, payload.LogId)

		p.ReceiveStream(smsg.StreamMessage{
			Type:           smsg.Ready,
			SequenceNumber: 0,
			Content:        "",
		})
		/*
			streamMessageToSend := portforward.KubePortForwardStreamMessageContent{
				PortForwardRequestId: "pid",
				Content:              []byte(testData),
			}
			streamMessageToSendBytes, _ := json.Marshal(streamMessageToSend)
			encodedContent := base64.StdEncoding.EncodeToString(streamMessageToSendBytes)
			p.ReceiveStream(smsg.StreamMessage{
				SequenceNumber: 1,
				Content:        encodedContent,
				Type:           smsg.Data,
			})
		*/

		tmb.Kill(errors.New("test kill"))

		time.Sleep(time.Second)
		writer.AssertExpectations(t)
	}()

	err := p.Start(&tmb, &writer, &request)
	assert.Nil(err)
}

func TestPortForwardError(t *testing.T) {
	assert := assert.New(t)
	var tmb tomb.Tomb
	logger := testutils.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "logs"
	sendData := "send data"
	urlPath := "test-path"
	errorStr := "test error"
	// TODO: check validity
	p, outputChan := New(logger, requestId, logId, command)

	request := testutils.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)

	writer := testutils.MockResponseWriter{}
	writer.On("WriteHeader", http.StatusForbidden).Return()
	writer.On("Header").Return(make(map[string][]string))
	// not crazy about this since we have to reuse code from the file we're testing
	errorJson, _ := json.Marshal(api.Status{
		Message: errorStr,
		Status:  api.StatusFailure,
		Code:    http.StatusForbidden,
		Reason:  "Forbidden",
	})
	// TODO: 0?
	writer.On("Write", errorJson).Return(0, nil)

	go func() {
		// get initial payload from output channel
		reqMessage := <-outputChan
		assert.Equal(string(portforward.StartPortForward), reqMessage.Action)
		var payload portforward.KubePortForwardStartActionPayload
		err := json.Unmarshal(reqMessage.ActionPayload, &payload)
		assert.Nil(err)
		assert.Equal(command, payload.CommandBeingRun)
		assert.Equal(requestId, payload.RequestId)
		assert.Equal(logId, payload.LogId)

		p.ReceiveStream(smsg.StreamMessage{
			Type:    smsg.Ready,
			Content: errorStr,
		})

		reqMessage = <-outputChan
		assert.Equal(string(portforward.StopPortForward), reqMessage.Action)
		var stopPayload portforward.KubePortForwardStopActionPayload
		err = json.Unmarshal(reqMessage.ActionPayload, &stopPayload)
		assert.Nil(err)
		assert.Equal(requestId, stopPayload.RequestId)
		assert.Equal(logId, stopPayload.LogId)

		time.Sleep(time.Second)
		writer.AssertExpectations(t)
	}()

	err := p.Start(&tmb, &writer, &request)
	assert.Equal(fmt.Errorf("error starting portforward stream: %s", errorStr), err)
}
