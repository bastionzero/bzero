package mocks

import (
	"bytes"
	"encoding/base64"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/apimachinery/pkg/util/httpstream"
)

func MockLogger() *logger.Logger {
	logger, err := logger.New(logger.DefaultLoggerConfig(logger.Debug.String()), "/dev/null")
	if err == nil {
		return logger
	}
	return nil
}

// FIXME: docstring
type MockResponseWriter struct {
	mock.Mock
	http.ResponseWriter
}

func (m MockResponseWriter) Header() http.Header {
	args := m.Called()
	return args.Get(0).(map[string][]string)
}

func (m MockResponseWriter) Write(content []byte) (int, error) {
	args := m.Called(content)
	return args.Int(0), args.Error(1)
}

func (m MockResponseWriter) WriteHeader(statusCode int) {
	m.Called(statusCode)
}

// FIXME: docstring
type MockStream struct {
	mock.Mock
	io.ReadWriteCloser
	MyStreamData string
}

func (m MockStream) Close() error {
	args := m.Called()
	return args.Error(0)
}
func (m MockStream) Read(p []byte) (n int, err error) {
	args := m.Called(p)

	// use test string
	copy(p, []byte(m.MyStreamData))

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
	return args.Get(0).(http.Header)
}
func (m MockStream) Identifier() uint32 {
	args := m.Called()
	return args.Get(0).(uint32)
}

// FIXME: docstring
type MockStreamConnection struct {
	mock.Mock
	httpstream.Connection
	headers http.Header
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

// create an http request with the specified details
func MockHttpRequest(method string, path string, headers map[string][]string, content string) http.Request {
	return http.Request{
		Method:        method,
		URL:           &url.URL{Path: path},
		Header:        headers,
		ContentLength: int64(len(content)),
		Body:          ioutil.NopCloser(bytes.NewBufferString(content)),
	}
}

// assert that the content of the stream message coming from outputChan is equal to testSTring
// this is a common pattern when testing the agent
func AssertNextMessageHasContent(assert *assert.Assertions, outputChan chan smsg.StreamMessage, testString string) {
	message := <-outputChan
	content, err := base64.StdEncoding.DecodeString(message.Content)
	assert.Nil(err)
	assert.Equal([]byte(testString), content)
}
