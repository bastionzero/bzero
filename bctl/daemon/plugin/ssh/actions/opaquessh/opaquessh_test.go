package opaquessh

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/bzio"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/tests"
)

func TestDefaultSsh(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Daemon OpaqueSsh Suite")
}

var _ = Describe("Daemon OpaqueSsh action", func() {
	logger := logger.MockLogger()
	identityFile := "testFile"
	testData := "testData"
	testOutput := "testOutput"

	Context("Happy path I: keys exist, closed by agent", func() {

		doneChan := make(chan struct{})
		outboxQueue := make(chan plugin.ActionWrapper, 1)

		mockFileService := bzio.MockBzFileIo{}
		// provide the action this demo (valid) private key
		mockFileService.On("ReadFile", identityFile).Return([]byte(tests.DemoPem), nil)

		mockIoService := bzio.MockBzIo{TestData: testData}
		mockIoService.On("Read").Return(len(testData), nil)
		mockIoService.On("Write", []byte(testOutput)).Return(len(testOutput), nil).Times(2)

		s := New(logger, outboxQueue, doneChan, identityFile, mockFileService, mockIoService)

		// NOTE: we can't make extensive use of the hierarchy here because we're evaluating messages being passed as state changes
		It("passes the SSH request to the agent and starts communicating with the local SSH process", func() {

			By("starting without error")
			err := s.Start()
			Expect(err).To(BeNil())

			By("sending an SshOpen request to the agent")
			openMsg := <-s.outboxQueue
			Expect(string(openMsg.Action)).To(Equal(string(ssh.SshOpen)))
			var openPayload ssh.SshOpenMessage
			err = json.Unmarshal(openMsg.ActionPayload, &openPayload)
			Expect(err).To(BeNil())
			// action should have successfully created a public key from the private one
			Expect(string(openPayload.PublicKey)).To(Equal(tests.DemoPub))

			By("sending whatever it reads from SSH to the agent")
			inputMsg := <-s.outboxQueue
			Expect(string(inputMsg.Action)).To(Equal(string(ssh.SshInput)))
			var inputPayload ssh.SshInputMessage
			err = json.Unmarshal(inputMsg.ActionPayload, &inputPayload)
			Expect(err).To(BeNil())
			Expect(string(inputPayload.Data)).To(Equal(testData))

			By("writing everything it receives from the agent back to SSH")

			content := base64.StdEncoding.EncodeToString([]byte(testOutput))

			s.ReceiveStream(smsg.StreamMessage{
				Type:    smsg.StdOut,
				Content: content,
				More:    true,
			})

			s.ReceiveStream(smsg.StreamMessage{
				Type:    smsg.StdOut,
				Content: content,
				More:    false,
			})

			<-doneChan

			mockFileService.AssertExpectations(GinkgoT())
			mockIoService.AssertExpectations(GinkgoT())
		})
	})

	Context("Happy path II: keys don't exist, closed by user", func() {

		doneChan := make(chan struct{})
		outboxQueue := make(chan plugin.ActionWrapper, 1)

		mockFileService := bzio.MockBzFileIo{}
		// provide the action an invalid private key -- this will force it to generate a new one
		mockFileService.On("ReadFile", identityFile).Return([]byte("invalid key"), nil)
		// ...which we expect to be written out
		mockFileService.On("WriteFile", identityFile).Return(nil)

		mockIoService := bzio.MockBzIo{TestData: testData}
		mockIoService.On("Read").Return(0, io.EOF)

		s := New(logger, outboxQueue, doneChan, identityFile, mockFileService, mockIoService)

		// NOTE: we can't make extensive use of the hierarchy here because we're evaluating messages being passed as state changes
		It("passes the SSH request to the agent and starts communicating with the local SSH process", func() {

			By("starting without error")
			err := s.Start()
			Expect(err).To(BeNil())

			By("sending an SshOpen request to the agent")
			openMsg := <-s.outboxQueue
			Expect(string(openMsg.Action)).To(Equal(string(ssh.SshOpen)))
			var openPayload ssh.SshOpenMessage
			err = json.Unmarshal(openMsg.ActionPayload, &openPayload)
			Expect(err).To(BeNil())
			// can't check the public key's contents but we can make sure it's there
			Expect(len(openPayload.PublicKey)).Should(BeNumerically(">", 0))

			By("stopping when the user ends the session")
			closeMsg := <-s.outboxQueue
			Expect(string(closeMsg.Action)).To(Equal(string(ssh.SshClose)))
			var closePayload ssh.SshCloseMessage
			err = json.Unmarshal(closeMsg.ActionPayload, &closePayload)
			Expect(err).To(BeNil())
			// action should have successfully created a public key from the private one
			Expect(string(closePayload.Reason)).To(Equal(endedByUser))

			<-doneChan

			mockFileService.AssertExpectations(GinkgoT())
			mockIoService.AssertExpectations(GinkgoT())
		})
	})
})
