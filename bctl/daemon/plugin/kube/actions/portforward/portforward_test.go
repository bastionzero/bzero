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

	"bastionzero.com/bctl/v1/bzerolib/mocks"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
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

func setGetUpgradedConnection(mockConnection *mocks.MockStreamConnection) (http.Header, http.Header) {
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
	logger := mocks.MockLogger()
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

	request := mocks.MockHttpRequest("GET", urlPath, headers, sendData)

	mockStreamConnection := mocks.MockStreamConnection{}
	mockStreamConnection.On("SetIdleTimeout", kubeutils.DefaultIdleTimeout).Return()
	var closeChan <-chan bool
	mockStreamConnection.On("CloseChan").Return(closeChan)
	mockStreamConnection.On("Close").Return(nil)

	writer := mocks.MockResponseWriter{}

	setPerformHandshake()
	dataHeaders, errorHeaders := setGetUpgradedConnection(&mockStreamConnection)

	mockDataStream := mocks.MockStream{MyStreamData: streamData}
	mockDataStream.On("Headers").Return(dataHeaders)
	mockDataStream.On("Close").Return(nil)
	mockDataStream.On("Read", make([]byte, portforward.DataStreamBufferSize)).Return(len(streamData), nil)
	mockDataStream.On("Write", []byte(testData)).Return(len(testData), nil).Times(3)

	mockErrorStream := mocks.MockStream{MyStreamData: streamError}
	mockErrorStream.On("Headers").Return(errorHeaders)
	mockErrorStream.On("Close").Return(nil)
	mockErrorStream.On("Read", make([]byte, portforward.ErrorStreamBufferSize)).Return(len(streamError), nil)
	mockErrorStream.On("Write", []byte(testData)).Return(len(testData), nil)

	mockStreamConnection.On("CreateStream", dataHeaders).Return(mockDataStream, nil)
	mockStreamConnection.On("CreateStream", errorHeaders).Return(mockErrorStream, nil)

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
			Type:    smsg.Ready,
			Content: "",
		})
		// FIXME: these might come in either order
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

		streamMessageToSend := portforward.KubePortForwardStreamMessageContent{
			PortForwardRequestId: "pid",
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

	err := p.Start(&tmb, &writer, &request)
	assert.Nil(err)
}

func TestPortForwardError(t *testing.T) {
	assert := assert.New(t)
	var tmb tomb.Tomb
	logger := mocks.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "logs"
	sendData := "send data"
	urlPath := "test-path"
	errorStr := "test error"
	// TODO: check validity
	p, outputChan := New(logger, requestId, logId, command)

	request := mocks.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)

	writer := mocks.MockResponseWriter{}
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

func checkDataInMessage(assert *assert.Assertions, msg plugin.ActionWrapper, testStr string) {
	var dataInPayload portforward.KubePortForwardActionPayload
	err := json.Unmarshal(msg.ActionPayload, &dataInPayload)
	assert.Nil(err)
	assert.Equal([]byte(testStr), dataInPayload.Data)
}
