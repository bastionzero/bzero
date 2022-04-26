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
	"gopkg.in/tomb.v2"
)

func TestRestApi(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Books Suite")
}

var _ = Describe("Daemon RestApi action", func() {
	var tmb tomb.Tomb
	logger := logger.MockLogger()
	requestId := "rid"
	logId := "lid"
	command := "get pods"
	sendData := "send data"
	receiveData := "receive data"
	urlPath := "test-path"

	Context("Receiving a successful API response", func() {
		request := tests.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)
		writer := tests.MockResponseWriter{}
		writer.On("Write", []byte(receiveData)).Return(len(receiveData), nil)
		r, outputChan := New(logger, requestId, logId, command)

		// NOTE: because Start() doesn't return until its work is done, we can't make extensive use of the DSL
		It("sends the correct RestApi payload to the agent, writes the API response to the user, and returns without error", func() {
			go func() {
				reqMessage := <-outputChan
				Expect(string(reqMessage.Action)).To(Equal(string(kuberest.RestRequest)))

				// payload should contain the user's request
				var payload kuberest.KubeRestApiActionPayload
				err := json.Unmarshal(reqMessage.ActionPayload, &payload)
				Expect(err).To(BeNil())
				Expect(payload.Body).To(Equal(sendData))

				payloadBytes, _ := json.Marshal(kuberest.KubeRestApiActionResponsePayload{
					Headers:    make(map[string][]string),
					Content:    []byte(receiveData),
					StatusCode: http.StatusOK,
				})

				r.ReceiveKeysplitting(plugin.ActionWrapper{
					ActionPayload: payloadBytes,
				})

				// this checks that the Write function was called as expected
				time.Sleep(1 * time.Second)
				writer.AssertExpectations(GinkgoT())
			}()

			err := r.Start(&tmb, &writer, &request)
			Expect(err).To(BeNil())
		})
	})

	Context("Receiving an API error", func() {
		request := tests.MockHttpRequest("GET", urlPath, make(map[string][]string), sendData)
		writer := tests.MockResponseWriter{}
		writer.On("Write", []byte(receiveData)).Return(len(receiveData), nil)
		writer.On("Header").Return(make(map[string][]string))
		writer.On("WriteHeader", http.StatusInternalServerError).Return()
		r, outputChan := New(logger, requestId, logId, command)

		// NOTE: because Start() doesn't return until its work is done, we can't make extensive use of the DSL
		It("sends the correct RestApi payload to the agent, writes the error to the user, and returns the error", func() {
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

				r.ReceiveKeysplitting(plugin.ActionWrapper{
					ActionPayload: payloadBytes,
				})

				// this checks that the Write function was called as expected
				time.Sleep(1 * time.Second)
				writer.AssertExpectations(GinkgoT())
			}()

			err := r.Start(&tmb, &writer, &request)
			Expect(err).To(Equal(fmt.Errorf("request failed with status code %v: %v", http.StatusNotFound, receiveData)))
		})
	})
})
