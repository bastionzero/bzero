package defaultssh

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	"bastionzero.com/bctl/v1/bzerolib/services/fileservice"
	"bastionzero.com/bctl/v1/bzerolib/services/ioservice"
)

func TestDefaultSsh(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Daemon DefaultSsh Suite")
}

var _ = Describe("Daemon DefaultSsh action", func() {
	logger := logger.MockLogger()
	identityFile := ""
	Context("Happy path, keys exist", func() {

		doneChan := make(chan struct{})
		outboxQueue := make(chan plugin.ActionWrapper, 1)

		mockFileService := fileservice.MockFileService{}
		// provide the action this demo (valid) private key
		mockFileService.On("ReadFile", identityFile).Return([]byte(demoPem), nil)

		mockIoService := ioservice.MockIoService{}
		mockIoService.On("Read").Return(10, nil)

		s := New(logger, outboxQueue, doneChan, identityFile, mockFileService, mockIoService)

		// NOTE: we can't make extensive use of the hierarchy here because we're evaluating messages being passed as state changes
		It("passes the SSH request to the agent and starts communicating with the local SSH process", func() {
			go func() {
				openMsg := <-s.outboxQueue

				By("sending an SshOpen request to the agent")
				Expect(string(openMsg.Action)).To(Equal(string(ssh.SshOpen)))
				var payload ssh.SshOpenMessage
				err := json.Unmarshal(openMsg.ActionPayload, &payload)
				Expect(err).To(BeNil())
				// action should have successfully created a public key from teh private one
				Expect(payload.PublicKey).To(Equal([]byte(demoPub)))

				By("sending whatever it reads from SSH to the agent")

				By("writing everything it receives from teh agent back to SSH")
			}()

			By("starting without error")
			err := s.Start()
			Expect(err).To(BeNil())
		})
	})
})
