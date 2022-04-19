package restapi

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"bastionzero.com/bctl/v1/bzerolib/testutils"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	flag.Parse()
	//oldMakeRequest := makeRequest
	//defer func() { makeRequest = oldMakeRequest }()

	exitCode := m.Run()

	// Exit
	os.Exit(exitCode)
}

func TestRestApi(t *testing.T) {
	assert := assert.New(t)
	logger := testutils.MockLogger()

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
	assert.Equal(kuberest.RestResponse, action)

	// restapi should return the response it got from the kube API
	expectedResponse := buildExpectedResponsePayload(statusCode, headers, requestId, testString, assert)
	assert.Equal(expectedResponse, responsePayload)

	// restapi should have closed
	assert.Equal(true, r.Closed())
}
