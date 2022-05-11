package restapi

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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

func TestRestApi(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent RestApi Suite")
}

var _ = Describe("Agent RestApi action", Ordered, func() {

	oldMakeRequest := makeRequest
	AfterAll(func() {
		makeRequest = oldMakeRequest
	})

	logger := logger.MockLogger()

	statusCode := 200
	requestId := "rid"
	testString := "Test body"
	headers := map[string][]string{
		"X-Kubernetes-Pf-Prioritylevel-Uid": {"value1"},
		"X-Kubernetes-Pf-Flowschema-Uid":    {"value2"},
	}

	Context("Happy path", func() {
		doneChan := make(chan struct{})
		setMakeRequest(statusCode, headers, testString)
		r := New(logger, doneChan, "serviceAccountToken", "kubeHost", make([]string, 0), "test user")

		It("handles the API request and response correctly", func() {
			By("receiving an API request without error")
			payloadBytes := buildActionPayload(make(map[string][]string), requestId)
			responsePayload, err := r.Receive("restapi", payloadBytes)
			Expect(err).To(BeNil())

			By("returning the expected response")
			expectedResponse := buildExpectedResponsePayload(statusCode, headers, requestId, testString)
			Expect(responsePayload).To(Equal(expectedResponse))
		})
	})
})
