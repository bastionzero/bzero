package restapi

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	kuberest "bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/restapi"
	"bastionzero.com/bctl/v1/bzerolib/tests"
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
	logger := logger.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "get pods"
	sendData := "send data"
	receiveData := "receive data"
	urlPath := "test-path"

	request := tests.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)

	writer := tests.MockResponseWriter{}
	writer.On("Write", []byte(receiveData)).Return(len(receiveData), nil)

	t.Logf("Test that we can create a new RestApi action")
	r, outputChan := New(logger, requestId, logId, command)

	// need a goroutine because Start won't return until we've received the message
	go func() {
		reqMessage := <-outputChan

		t.Logf("Test that it sends a RestApi payload to the agent")
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

		t.Logf("Test that it writes out a response to the user")
		r.ReceiveKeysplitting(plugin.ActionWrapper{
			ActionPayload: payloadBytes,
		})

		// give it time to process
		time.Sleep(1 * time.Second)
		writer.AssertExpectations(t)
	}()

	t.Logf("Test that we can start the action")
	err := r.Start(&tmb, &writer, &request)
	assert.Nil(err)
}

func TestRestApiNotFound(t *testing.T) {
	assert := assert.New(t)
	var tmb tomb.Tomb
	logger := logger.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "get pods"
	sendData := "send data"
	receiveData := "not found"
	urlPath := "test-path"

	t.Logf("Test that we can create a new RestApi action")
	r, outputChan := New(logger, requestId, logId, command)

	request := tests.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)

	writer := tests.MockResponseWriter{}
	writer.On("Write", []byte(receiveData)).Return(len(receiveData), nil)
	writer.On("Header").Return(make(map[string][]string))
	writer.On("WriteHeader", http.StatusInternalServerError).Return()

	// need a goroutine because Start won't return until we've received the message
	go func() {
		// can ignore this since we're not testing it
		<-outputChan

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

		// give it time to process
		time.Sleep(1 * time.Second)
		writer.AssertExpectations(t)
	}()

	t.Logf("Test that we can start the action")
	err := r.Start(&tmb, &writer, &request)
	t.Logf("Test that it returns an error if it receives one from the agent")
	assert.Equal(fmt.Errorf("request failed with status code %v: %v", http.StatusNotFound, receiveData), err)
}
