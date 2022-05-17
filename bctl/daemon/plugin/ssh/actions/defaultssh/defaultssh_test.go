package defaultssh

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDefaultSsh(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Daemon DefaultSsh Suite")
}

var _ = Describe("Daemon DefaultSsh action", func() {
	//logger := logger.MockLogger()
	//identityFile := ""
	/*
		Context("Happy path, keys exist", func() {

			doneChan := make(chan struct{})
			outboxQueue := make(chan plugin.ActionWrapper, 1)
			s := New(logger, outboxQueue, doneChan, identityFile, logId, command)

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
	*/
})
