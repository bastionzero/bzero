package restapi

import (
	"bytes"
	"encoding/json"
	"flag"
	"io/ioutil"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	kuberest "bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/restapi"
)

// what restapi action will receive from "bastion"
func buildActionPayload(headers map[string][]string, requestId string) []byte {
	payloadBytes, _ := json.Marshal(kuberest.KubeRestApiActionPayload{
		Endpoint:        "test/endpoint",
		Headers:         headers,
		Method:          "GET",
		Body:            "",
		RequestId:       requestId,
		LogId:           "lid",
		CommandBeingRun: "command",
	})
	return payloadBytes
}

// what restapi action will receive from "kube"
func buildExpectedResponsePayload(statusCode int, headers map[string][]string, requestId string, bodyText string) []byte {
	payloadBytes, _ := json.Marshal(kuberest.KubeRestApiActionResponsePayload{
		StatusCode: statusCode,
		RequestId:  requestId,
		Headers:    headers,
		Content:    []byte(bodyText),
	})
	return payloadBytes
}

// inject logic for what happens when restapi makes an HTTP request
func setMakeRequest(statusCode int, headers map[string][]string, bodyText string) {
	makeRequest = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: statusCode,
			Header:     headers,
			Body:       ioutil.NopCloser(bytes.NewBufferString(bodyText)),
		}, nil
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	oldMakeRequest := makeRequest
	defer func() { makeRequest = oldMakeRequest }()

	exitCode := m.Run()

	// Exit
	os.Exit(exitCode)
}

func TestRestApi(t *testing.T) {
	assert := assert.New(t)
	logger := logger.MockLogger()

	statusCode := 200
	requestId := "rid"
	testString := "Test body"
	headers := map[string][]string{
		"X-Kubernetes-Pf-Prioritylevel-Uid": {"value1"},
		"X-Kubernetes-Pf-Flowschema-Uid":    {"value2"},
	}

	setMakeRequest(statusCode, headers, testString)

	t.Logf("Test that we can create a new RestApi action")
	r, err := New(logger, "serviceAccountToken", "kubeHost", make([]string, 0), "test user")
	assert.Nil(err)

	payloadBytes := buildActionPayload(make(map[string][]string), requestId)
	t.Logf("Test that we can ask the action to make an API request")
	action, responsePayload, err := r.Receive("restapi", payloadBytes)
	assert.Nil(err)
	assert.Equal(string(kuberest.RestResponse), action)

	t.Logf("Test that it returns the expected response to that request")
	expectedResponse := buildExpectedResponsePayload(statusCode, headers, requestId, testString)
	assert.Equal(expectedResponse, responsePayload)

	t.Logf("Test that the action has closed")
	assert.Equal(true, r.Closed())
}
