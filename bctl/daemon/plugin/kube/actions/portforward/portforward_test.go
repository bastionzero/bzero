package portforward

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/tests"
	"github.com/stretchr/testify/assert"
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

func TestMain(m *testing.M) {
	flag.Parse()
	oldGetUpgradedConnection := getUpgradedConnection
	oldPerformHandshake := performHandshake
	defer func() {
		getUpgradedConnection = oldGetUpgradedConnection
		performHandshake = oldPerformHandshake
	}()

	exitCode := m.Run()

	// Exit
	os.Exit(exitCode)
}

func TestPortForward(t *testing.T) {
	assert := assert.New(t)
	var tmb tomb.Tomb
	logger := logger.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "logs"
	sendData := "send data"
	testData := "receive data"
	streamData := "stream data"
	streamError := "stream error"
	urlPath := "test-path"

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

	t.Logf("Test that we can create a new PortForward action")
	p, outputChan := New(logger, requestId, logId, command)

	go func() {
		startMessage := <-outputChan

		t.Logf("Test that it sends a PortForward payload to the agent")
		assert.Equal(string(portforward.StartPortForward), startMessage.Action)
		var payload portforward.KubePortForwardStartActionPayload
		err := json.Unmarshal(startMessage.ActionPayload, &payload)
		assert.Nil(err)
		assert.Equal(command, payload.CommandBeingRun)
		assert.Equal(requestId, payload.RequestId)
		assert.Equal(logId, payload.LogId)

		t.Logf("Test that it listens for data and error streams upon receiving the ready message")
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
				assert.Nil(err)
				assert.Equal([]byte(streamData), dataInPayload.Data)
				receivedDataIn = true
			case string(portforward.ErrorPortForward):
				var errorInPayload portforward.KubePortForwardActionPayload
				err = json.Unmarshal(msg.ActionPayload, &errorInPayload)
				assert.Nil(err)
				assert.Equal([]byte(streamError), errorInPayload.Data)
				receivedError = true
			}
			n++
		}
		assert.True(receivedDataIn)
		assert.True(receivedError)

		t.Logf("Test that it processes data and error messages in the correct order")
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

		t.Logf("Test that we can kill its tomb and make it stop")
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
		assert.True(receivedStopRequestMsg)
		assert.True(receivedStopMsg)

		writer.AssertExpectations(t)
	}()

	t.Logf("Test that we can start the action")
	err := p.Start(&tmb, &writer, &request)
	assert.Nil(err)
}

func TestPortForwardError(t *testing.T) {
	assert := assert.New(t)
	var tmb tomb.Tomb
	logger := logger.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "logs"
	sendData := "send data"
	urlPath := "test-path"
	errorStr := "test error"

	request := tests.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)

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

	go func() {
		// can ignore this since we're not testing it
		<-outputChan

		t.Logf("Test that it stop if the ready message contains an error")
		p.ReceiveStream(smsg.StreamMessage{
			Type:    smsg.Ready,
			Content: errorStr,
		})

		stopMessage := <-outputChan
		assert.Equal(string(portforward.StopPortForward), stopMessage.Action)
		var stopPayload portforward.KubePortForwardStopActionPayload
		err := json.Unmarshal(stopMessage.ActionPayload, &stopPayload)
		assert.Nil(err)
		assert.Equal(requestId, stopPayload.RequestId)
		assert.Equal(logId, stopPayload.LogId)

		// give it time to run
		time.Sleep(time.Second)
		writer.AssertExpectations(t)
	}()

	err := p.Start(&tmb, &writer, &request)
	assert.Equal(fmt.Errorf("error starting portforward stream: %s", errorStr), err)
}
