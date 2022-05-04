package restapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	kuberest "bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/restapi"
	"bastionzero.com/bctl/v1/bzerolib/tests"
)

func TestRestApi(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Daemon RestApi Suite")
}

var _ = Describe("Daemon RestApi action", func() {
	logger := logger.MockLogger()

	requestId := "rid"
	logId := "lid"
	command := "get pods"
	sendData := "send data"
	receiveData := "receive data"
	urlPath := "test-path"

	Context("Receiving a successful API response", func() {

		doneChan := make(chan struct{})
		outputChan := make(chan plugin.ActionWrapper, 1)
		request := tests.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)
		writer := tests.MockResponseWriter{}
		writer.On("Write", []byte(receiveData)).Return(len(receiveData), nil)
		r := New(logger, outputChan, doneChan, requestId, logId, command)

		// NOTE: we can't make extensive use of the hierarchy here because we're evaluating messages being passed as state changes
		It("passes the API request and response correctly", func() {
			go func() {
				reqMessage := <-outputChan

				By("sending a RestApi payload to the agent that contains the user's request")
				Expect(string(reqMessage.Action)).To(Equal(string(kuberest.RestRequest)))
				var payload kuberest.KubeRestApiActionPayload
				err := json.Unmarshal(reqMessage.ActionPayload, &payload)
				Expect(err).To(BeNil())
				Expect(payload.Body).To(Equal(sendData))

				payloadBytes, _ := json.Marshal(kuberest.KubeRestApiActionResponsePayload{
					Headers:    make(map[string][]string),
					Content:    []byte(receiveData),
					StatusCode: http.StatusOK,
				})

				r.ReceiveKeysplitting(payloadBytes)

				By("writing the API response out to the user")
				time.Sleep(1 * time.Second)
				writer.AssertExpectations(GinkgoT())
			}()

			By("starting without error")
			err := r.Start(&writer, &request)
			Expect(err).To(BeNil())
		})
	})

	Context("Receiving an API error", func() {
		doneChan := make(chan struct{})
		outputChan := make(chan plugin.ActionWrapper, 1)
		request := tests.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)
		writer := tests.MockResponseWriter{}
		writer.On("Write", []byte(receiveData)).Return(len(receiveData), nil)
		writer.On("Header").Return(make(map[string][]string))
		writer.On("WriteHeader", http.StatusInternalServerError).Return()
		r := New(logger, outputChan, doneChan, requestId, logId, command)

		// NOTE: we can't make extensive use of the hierarchy here because we're evaluating messages being passed as state changes
		It("handles the API request and response correctly, given the error", func() {
			go func() {
				// we can ignore this since we're not testing it
				<-outputChan

				payloadBytes, _ := json.Marshal(kuberest.KubeRestApiActionResponsePayload{
					Headers: map[string][]string{
						"Content-Length": {"12"},
						"Origin":         {"val1", "val2"},
					},
					Content:    []byte(receiveData),
					StatusCode: http.StatusNotFound,
				})

				r.ReceiveKeysplitting(payloadBytes)

				// this checks that the Write function was called as expected
				By("returning the error to the user")
				time.Sleep(1 * time.Second)
				writer.AssertExpectations(GinkgoT())
			}()

			By("returning the error to the datachannel")
			err := r.Start(&writer, &request)
			Expect(err).To(Equal(fmt.Errorf("request failed with status code %v: %v", http.StatusNotFound, receiveData)))
		})
	})
})
