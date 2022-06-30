package transparentssh

import (
	"net"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gossh "golang.org/x/crypto/ssh"

	"bastionzero.com/bctl/v1/bzerolib/bzio"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	"bastionzero.com/bctl/v1/bzerolib/tests"
)

func TestDefaultSsh(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Daemon TransparentSsh Suite")
}

var _ = Describe("Daemon TransparentSsh action", func() {
	logger := logger.MockLogger()
	identityFile := "testFile"
	testData := "testData"
	//testOutput := "testOutput"

	Context("Happy path I: keys exist, closed by agent", func() {

		doneChan := make(chan struct{})
		outboxQueue := make(chan plugin.ActionWrapper, 1)

		mockFileService := bzio.MockBzFileIo{}
		// provide the action this demo (valid) private key
		mockFileService.On("ReadFile", identityFile).Return([]byte(tests.DemoPem), nil)

		mockIoService := bzio.MockBzIo{TestData: testData}
		mockIoService.On("Write", []byte(readyMsg)).Return(len(readyMsg), nil)

		// I guess I then need to make a client that also talks to this?
		listener, _ := net.Listen("tcp", ":22222")

		s := New(logger, outboxQueue, doneChan, identityFile, mockFileService, mockIoService, listener)
		// NOTE: we can't make extensive use of the hierarchy here because we're evaluating messages being passed as state changes
		It("passes the SSH request to the agent and starts communicating with the local SSH process", func() {

			By("starting without error")
			err := s.Start()
			Expect(err).To(BeNil())

			// it will write to stdout here

			// then it will start listening for connections
			privateBytes, _, _ := bzssh.GenerateKeys()

			signer, _ := gossh.ParsePrivateKey(privateBytes)

			config := &gossh.ClientConfig{
				User:            "testUser",
				HostKeyCallback: gossh.InsecureIgnoreHostKey(),
				Auth: []gossh.AuthMethod{
					gossh.PublicKeys(signer),
				},
			}

			_, err = gossh.Dial("tcp", "localhost:22222", config)
			Expect(err).To(BeNil())

			// now what? I guess I follow the agent's lead:
			// - get a session, start some pipes
			// - write bytes, make sure the daemon writes them

		})

	})
})
