package testutils

import (
	"io"
	"net/http"
	"time"

	"github.com/stretchr/testify/mock"
	"k8s.io/apimachinery/pkg/util/httpstream"
)

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
