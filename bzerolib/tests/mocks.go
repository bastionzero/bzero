package tests

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"

	"github.com/stretchr/testify/mock"
	"k8s.io/apimachinery/pkg/util/httpstream"
)

// MockResponseWriter can be injected into code that writes output (usually daemon-side)
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

// MockStreamConnection can be injected into code that reads from or writes to an http stream
// when implemented it should usually return a MockStream from calls to CreateStream
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
	return args.Get(0).(chan bool)
}
func (m *MockStreamConnection) SetIdleTimeout(timeout time.Duration) {
	m.Called(timeout)
}

// MockStream can be returned from a MockStreamConnection
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
	time.Sleep(time.Second)
	// we actually don't want to track this exactly because it leads to pathological behavior on multiple reads
	args := m.Called()

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
