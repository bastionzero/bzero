package restapi

import (
	"bytes"
	"encoding/json"
	"flag"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/plugin"
	kuberest "bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/restapi"
	"bastionzero.com/bctl/v1/bzerolib/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"gopkg.in/tomb.v2"
)

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
	m.Called()
}

func TestMain(m *testing.M) {
	flag.Parse()
	//oldMakeRequest := makeRequest
	//defer func() { makeRequest = oldMakeRequest }()

	exitCode := m.Run()

	// Exit
	os.Exit(exitCode)
}

func TestRestApiOK(t *testing.T) {
	assert := assert.New(t)
	var tmb tomb.Tomb
	logger := testutils.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "get pod"
	r, outputChan := New(logger, requestId, logId, command)

	request := http.Request{
		URL:           &url.URL{Path: "test-path"},
		Header:        make(map[string][]string),
		ContentLength: 8,
		Body:          ioutil.NopCloser(bytes.NewBufferString("request?")),
	}

	writer := MockResponseWriter{}
	writer.On("Write", []byte("Back atcha")).Return(10, nil)

	// need a goroutine because this won't return until we've received the message
	// so, could put the rest in a goroutine and actually call this
	go r.Start(&tmb, &writer, &request)

	reqMessage := <-outputChan

	assert.Equal(kuberest.RestRequest, reqMessage.Action)
	var payload kuberest.KubeRestApiActionPayload
	err := json.Unmarshal(reqMessage.ActionPayload, &payload)
	assert.Nil(err)
	assert.Equal("request?", payload.Body)

	payloadBytes, _ := json.Marshal(kuberest.KubeRestApiActionResponsePayload{
		Headers:    make(map[string][]string), // TODO:
		Content:    []byte("Back atcha"),
		StatusCode: http.StatusOK,
	})

	r.ReceiveKeysplitting(plugin.ActionWrapper{
		ActionPayload: payloadBytes,
	})

	time.Sleep(3 * time.Second)
}

/*
func TestRestApiNotFound(t *testing.T) {
	assert := assert.New(t)
	var tmb tomb.Tomb
	logger := testutils.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "get pod"
	r, outputChan := New(logger, requestId, logId, command)

	request := http.Request{
		URL:           &url.URL{Path: "test-path"},
		Header:        make(map[string][]string),
		ContentLength: 8,
		Body:          ioutil.NopCloser(bytes.NewBufferString("request?")),
	}

	writer := MockResponseWriter{}
	writer.On("Write", []byte("Back atcha 2")).Return(12, nil)

	// need a goroutine because this won't return until we've received the message
	// so, could put the rest in a goroutine and actually call this
	go r.Start(&tmb, &writer, &request)

	reqMessage := <-outputChan

	assert.Equal(kuberest.RestRequest, reqMessage.Action)
	var payload kuberest.KubeRestApiActionPayload
	err := json.Unmarshal(reqMessage.ActionPayload, &payload)
	assert.Nil(err)
	assert.Equal("request?", payload.Body)

	payloadBytes, _ := json.Marshal(kuberest.KubeRestApiActionResponsePayload{
		Headers:    make(map[string][]string), // TODO:
		Content:    []byte("Back atcha 2"),
		StatusCode: http.StatusOK,
	})

	r.ReceiveKeysplitting(plugin.ActionWrapper{
		ActionPayload: payloadBytes,
	})

	time.Sleep(3 * time.Second)
}
*/
