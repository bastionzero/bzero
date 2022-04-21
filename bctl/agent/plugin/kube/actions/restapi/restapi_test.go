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

	"bastionzero.com/bctl/v1/bzerolib/mocks"
	kuberest "bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/restapi"
)

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

func buildActionPayload(headers map[string][]string, requestId string) kuberest.KubeRestApiActionPayload {
	return kuberest.KubeRestApiActionPayload{
		Endpoint:        "test/endpoint",
		Headers:         headers,
		Method:          "GET",
		Body:            "",
		RequestId:       requestId,
		LogId:           "lid",
		CommandBeingRun: "command",
	}
}

func buildExpectedResponsePayload(statusCode int, headers map[string][]string, requestId string, bodyText string, assert *assert.Assertions) []byte {
	resultBytes, err := json.Marshal(kuberest.KubeRestApiActionResponsePayload{
		StatusCode: statusCode,
		RequestId:  requestId,
		Headers:    headers,
		Content:    []byte(bodyText),
	})
	assert.Nil(err)
	return resultBytes
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
	logger := mocks.MockLogger()

	statusCode := 200
	requestId := "rid"
	testString := "Test body"
	headers := map[string][]string{
		"X-Kubernetes-Pf-Prioritylevel-Uid": {"value1"},
		"X-Kubernetes-Pf-Flowschema-Uid":    {"value2"},
	}

	setMakeRequest(statusCode, headers, testString)

	r, err := New(logger, "serviceAccountToken", "kubeHost", make([]string, 0), "test user")
	assert.Nil(err)

	payload := buildActionPayload(make(map[string][]string), requestId)
	payloadBytes, err := json.Marshal(payload)
	assert.Nil(err)

	action, responsePayload, err := r.Receive("restapi", payloadBytes)
	assert.Nil(err)
	assert.Equal(string(kuberest.RestResponse), action)

	// restapi should return the response it got from the kube API
	expectedResponse := buildExpectedResponsePayload(statusCode, headers, requestId, testString, assert)
	assert.Equal(expectedResponse, responsePayload)

	// restapi should have closed
	assert.Equal(true, r.Closed())
}
