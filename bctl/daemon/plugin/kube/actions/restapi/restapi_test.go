package restapi

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/plugin"
	kuberest "bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/restapi"
	"bastionzero.com/bctl/v1/bzerolib/testutils"
	"github.com/stretchr/testify/assert"
	"gopkg.in/tomb.v2"
)

func TestMain(m *testing.M) {
	flag.Parse()

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
	command := "get pods"
	sendData := "send data"
	receiveData := "receive data"
	urlPath := "test-path"
	r, outputChan := New(logger, requestId, logId, command)

	request := testutils.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)

	writer := testutils.MockResponseWriter{}
	writer.On("Write", []byte(receiveData)).Return(12, nil)

	// need a goroutine because Start won't return until we've received the message
	go func() {
		reqMessage := <-outputChan

		assert.Equal(string(kuberest.RestRequest), reqMessage.Action)
		var payload kuberest.KubeRestApiActionPayload
		err := json.Unmarshal(reqMessage.ActionPayload, &payload)
		assert.Nil(err)
		assert.Equal(sendData, payload.Body)
		assert.Equal(command, payload.CommandBeingRun)
		assert.Equal(requestId, payload.RequestId)
		assert.Equal(logId, payload.LogId)

		payloadBytes, _ := json.Marshal(kuberest.KubeRestApiActionResponsePayload{
			Headers:    make(map[string][]string),
			Content:    []byte(receiveData),
			StatusCode: http.StatusOK,
		})

		r.ReceiveKeysplitting(plugin.ActionWrapper{
			ActionPayload: payloadBytes,
		})

		// FIXME: address race condition here
		time.Sleep(1 * time.Second)
		writer.AssertExpectations(t)
	}()

	err := r.Start(&tmb, &writer, &request)
	assert.Nil(err)
}

func TestRestApiNotFound(t *testing.T) {
	assert := assert.New(t)
	var tmb tomb.Tomb
	logger := testutils.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "get pods"
	sendData := "send data"
	receiveData := "receive data 2"
	urlPath := "test-path"
	r, outputChan := New(logger, requestId, logId, command)

	request := testutils.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)

	writer := testutils.MockResponseWriter{}
	writer.On("Write", []byte(receiveData)).Return(12, nil)
	writer.On("Header").Return(make(map[string][]string))
	writer.On("WriteHeader", http.StatusInternalServerError).Return()

	// need a goroutine because Start won't return until we've received the message
	go func() {
		reqMessage := <-outputChan

		assert.Equal(string(kuberest.RestRequest), reqMessage.Action)
		var payload kuberest.KubeRestApiActionPayload
		err := json.Unmarshal(reqMessage.ActionPayload, &payload)
		assert.Nil(err)
		assert.Equal(sendData, payload.Body)
		assert.Equal(command, payload.CommandBeingRun)
		assert.Equal(requestId, payload.RequestId)
		assert.Equal(logId, payload.LogId)

		payloadBytes, _ := json.Marshal(kuberest.KubeRestApiActionResponsePayload{
			Headers: map[string][]string{
				"Content-Length": {"12"},
				"Origin":         {"val1", "val2"},
			},
			Content:    []byte(receiveData),
			StatusCode: http.StatusNotFound,
		})

		r.ReceiveKeysplitting(plugin.ActionWrapper{
			ActionPayload: payloadBytes,
		})

		// FIXME: address race condition here
		time.Sleep(1 * time.Second)
		writer.AssertExpectations(t)
	}()

	err := r.Start(&tmb, &writer, &request)
	assert.Equal(fmt.Errorf("request failed with status code %v: %v", http.StatusNotFound, receiveData), err)
}
