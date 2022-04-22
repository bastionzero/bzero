package portforward

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"os"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/mocks"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"github.com/stretchr/testify/assert"
	"gopkg.in/tomb.v2"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
)

// what portforward action will receive from "bastion"
func buildStartActionPayload(assert *assert.Assertions, headers map[string][]string, requestId string, version smsg.SchemaVersion) []byte {
	payload := portforward.KubePortForwardStartActionPayload{
		Endpoint:             "test/endpoint",
		DataHeaders:          make(map[string]string),
		ErrorHeaders:         make(map[string]string),
		RequestId:            requestId,
		StreamMessageVersion: version,
		LogId:                "lid",
		CommandBeingRun:      "command",
	}
	payloadBytes, err := json.Marshal(payload)
	assert.Nil(err)
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
func setDoDial(streamConnection *mocks.MockStreamConnection) {
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

func TestMain(m *testing.M) {
	flag.Parse()
	oldDoDial := doDial
	defer func() { doDial = oldDoDial }()

	exitCode := m.Run()

	// Exit
	os.Exit(exitCode)
}

func TestPortforward(t *testing.T) {
	assert := assert.New(t)
	logger := mocks.MockLogger()
	var tmb tomb.Tomb
	outputChan := make(chan smsg.StreamMessage, 1)

	requestId := "rid"
	portForwardRequestId := "pid"
	testData := "test data"

	mockStream := mocks.MockStream{MyStreamData: testData}
	mockStream.On("Read", make([]byte, portforward.DataStreamBufferSize)).Return(9, nil)
	mockStream.On("Write", []byte(testData)).Return(len(testData), nil)
	mockStream.On("Close").Return(nil)

	mockStreamConnection := new(mocks.MockStreamConnection)
	mockStreamConnection.On("CreateStream", http.Header{
		"Port":      []string{"5000"},
		"Requestid": []string{portForwardRequestId},
	}).Return(mockStream, nil)
	mockStreamConnection.On("Close").Return(nil)

	setDoDial(mockStreamConnection)
	setGetConfig()

	t.Logf("Test that we can create a new PortForward action")
	p, err := New(logger, &tmb, "serviceAccountToken", "kubeHost", make([]string, 0), "test user", outputChan)
	assert.Nil(err)

	t.Logf("Test that we can initiate a portforward session")
	payload := buildStartActionPayload(assert, make(map[string][]string), requestId, smsg.CurrentSchema)
	action, responsePayload, err := p.Receive(string(portforward.StartPortForward), payload)
	assert.Nil(err)
	assert.Equal(string(portforward.StartPortForward), action)
	assert.Equal([]byte{}, responsePayload)

	readyMessage := <-outputChan
	t.Logf("Test that the action has started the portforward interaction with the kube server")
	assert.Equal(requestId, readyMessage.RequestId)
	assert.Equal("", readyMessage.Content)

	payload = buildActionPayload(testData, requestId, portForwardRequestId)
	t.Logf("Test that the action can accept input from the remote port")
	action, responsePayload, err = p.Receive(string(portforward.DataInPortForward), payload)
	assert.Nil(err)
	assert.Equal(string(portforward.DataInPortForward), action)
	assert.Equal([]byte{}, responsePayload)

	dataMessage := <-outputChan

	t.Logf("Test that the action forwards that data")
	wrappedContent, err := base64.StdEncoding.DecodeString(dataMessage.Content)
	assert.Nil(err)
	var content portforward.KubePortForwardStreamMessageContent
	err = json.Unmarshal(wrappedContent, &content)
	assert.Nil(err)
	assert.Equal(testData, string(content.Content))

	t.Logf("Test that we can end a portforward request")
	stopRequestPayload := buildStopRequestActionPayload(requestId, portForwardRequestId)
	action, responsePayload, err = p.Receive(string(portforward.StopPortForwardRequest), stopRequestPayload)
	assert.Nil(err)
	assert.Equal(string(portforward.StopPortForwardRequest), action)
	assert.Equal([]byte{}, responsePayload)

	t.Logf("Test that we can close the portforward action")
	stopPayload := buildStopActionPayload(requestId)
	action, responsePayload, err = p.Receive(string(portforward.StopPortForward), stopPayload)
	assert.Nil(err)
	assert.Equal(string(portforward.StopPortForward), action)
	assert.Equal([]byte{}, responsePayload)
	assert.True(p.Closed())

	t.Logf("Test that we can kill the action's tomb and stop it")
	tmb.Kill(errors.New("test kill"))

	time.Sleep(time.Second)

	mockStream.AssertExpectations(t)
	mockStreamConnection.AssertExpectations(t)
}
