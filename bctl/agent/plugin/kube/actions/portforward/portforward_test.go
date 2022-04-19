package portforward

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/textproto"
	"os"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"gopkg.in/tomb.v2"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
)

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

func buildActionPayload(assert *assert.Assertions, requestId string) []byte {
	payload := portforward.KubePortForwardActionPayload{
		RequestId:            requestId,
		LogId:                "lid",
		Data:                 []byte("test data"),
		PortForwardRequestId: "", // TODO: could make this better
		PodPort:              5000,
	}
	payloadBytes, err := json.Marshal(payload)
	assert.Nil(err)
	return payloadBytes
}

type MockStream struct {
	mock.Mock
	io.ReadWriteCloser
	myStreamData string
}

func (m MockStream) Close() error {
	args := m.Called()
	return args.Error(0)
}
func (m MockStream) Read(p []byte) (n int, err error) {
	args := m.Called(p)

	// use test string
	copy(p, []byte(m.myStreamData))

	return args.Int(0), args.Error(1)
}
func (m MockStream) Write(p []byte) (n int, err error) {
	args := m.Called(p)
	return args.Int(0), args.Error(1)
}
func (m MockStream) Reset() error {
	args := m.Called()
	return args.Error(0)
}
func (m MockStream) Headers() http.Header {
	args := m.Called()
	return args.Get(0).(map[string][]string)
}
func (m MockStream) Identifier() uint32 {
	args := m.Called()
	return args.Get(0).(uint32)
}

type MockStreamConnection struct {
	mock.Mock
	httpstream.Connection
}

func (m *MockStreamConnection) CreateStream(headers http.Header) (httpstream.Stream, error) {
	args := m.Called(headers)
	return args.Get(0).(httpstream.Stream), args.Error(1)
}
func (m *MockStreamConnection) Close() error {
	args := m.Called()
	return args.Error(0)
}
func (m *MockStreamConnection) CloseChan() <-chan bool {
	args := m.Called()
	return args.Get(0).(<-chan bool)
}
func (m *MockStreamConnection) SetIdleTimeout(timeout time.Duration) {
	m.Called(timeout)
}

func setDoDial(streamConnection *MockStreamConnection) {
	doDial = func(dialer httpstream.Dialer, protocolName string) (httpstream.Connection, string, error) {
		return streamConnection, "", nil
	}
}
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
	logger := testutils.MockLogger()
	var tmb tomb.Tomb
	outputChan := make(chan smsg.StreamMessage, 1)

	requestId := "rid"
	testData := "test data"

	mockStream := MockStream{myStreamData: testData}
	mockStream.On("Read", make([]byte, portforward.DataStreamBufferSize)).Return(9, nil)
	mockStream.On("Write", []byte(testData)).Return(6, nil)
	mockStream.On("Close").Return(nil)

	mockStreamConnection := new(MockStreamConnection)
	mockStreamConnection.On("CreateStream", http.Header{
		textproto.CanonicalMIMEHeaderKey(kubeutils.PortHeader):                 []string{"5000"},
		textproto.CanonicalMIMEHeaderKey(kubeutils.PortForwardRequestIDHeader): []string{""},
	}).Return(mockStream, nil)

	setDoDial(mockStreamConnection)
	setGetConfig()

	// test new
	p, err := New(logger, &tmb, "serviceAccountToken", "kubeHost", make([]string, 0), "test user", outputChan)
	assert.Nil(err)

	// test start
	payload := buildStartActionPayload(assert, make(map[string][]string), requestId, smsg.CurrentSchema)
	action, responsePayload, err := p.Receive(string(portforward.StartPortForward), payload)
	assert.Nil(err)
	assert.Equal(string(portforward.StartPortForward), action)
	assert.Equal([]byte{}, responsePayload)

	// ready message should come back
	readyMessage := <-outputChan
	assert.Equal(requestId, readyMessage.RequestId)
	assert.Equal("", readyMessage.Content)

	// test dataIn
	payload = buildActionPayload(assert, requestId)
	action, responsePayload, err = p.Receive(string(portforward.DataInPortForward), payload)
	assert.Nil(err)
	assert.Equal(string(portforward.DataInPortForward), action)
	assert.Equal([]byte{}, responsePayload)

	dataMessage := <-outputChan

	wrappedContent, err := base64.StdEncoding.DecodeString(dataMessage.Content)
	assert.Nil(err)
	var content portforward.KubePortForwardStreamMessageContent
	err = json.Unmarshal(wrappedContent, &content)
	assert.Nil(err)
	assert.Equal(testData, string(content.Content))
}
