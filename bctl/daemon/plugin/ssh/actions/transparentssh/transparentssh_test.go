package transparentssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gossh "golang.org/x/crypto/ssh"

	"bastionzero.com/bctl/v1/bzerolib/bzio"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
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

	var config *gossh.ClientConfig

	BeforeEach(func() {
		privateBytes, _, _ := bzssh.GenerateKeys()
		signer, _ := gossh.ParsePrivateKey(privateBytes)
		config = &gossh.ClientConfig{
			User:            "testUser",
			HostKeyCallback: gossh.InsecureIgnoreHostKey(),
			Auth: []gossh.AuthMethod{
				gossh.PublicKeys(signer),
			},
		}
	})

	Context("rejects unauthorized requests", func() {
		It("rejects an invalid exec request", func() {
			badScp := "scpfake"
			badScpErrMsg := bzssh.UnauthorizedCommandError(fmt.Sprintf("'%s'", badScp))

			doneChan := make(chan struct{})
			outboxQueue := make(chan plugin.ActionWrapper, 1)

			mockFileService := bzio.MockBzFileIo{}
			// provide the action this demo (valid) private key
			mockFileService.On("ReadFile", identityFile).Return([]byte(tests.DemoPem), nil)

			mockIoService := bzio.MockBzIo{TestData: testData}
			mockIoService.On("Write", []byte(readyMsg)).Return(len(readyMsg), nil)
			mockIoService.On("WriteErr", []byte(badScpErrMsg)).Return(len(badScpErrMsg), nil)

			// I guess I then need to make a client that also talks to this?
			listener, _ := net.Listen("tcp", ":22222")

			s := New(logger, outboxQueue, doneChan, identityFile, mockFileService, mockIoService, listener)

			By("starting without error")
			err := s.Start()
			Expect(err).To(BeNil())

			By("executing the SSH handshake")
			conn, err := gossh.Dial("tcp", "localhost:22222", config)
			Expect(err).To(BeNil())
			defer conn.Close()

			session, err := conn.NewSession()
			Expect(err).To(BeNil())

			By("rejecting the invalid request")
			ok, err := session.SendRequest("exec", true, []byte(fmt.Sprintf("\u0000\u0000\u0000\u0007%s", badScp)))
			Expect(err).To(Equal(io.EOF))
			Expect(ok).To(BeFalse())

			mockFileService.AssertExpectations(GinkgoT())
			mockIoService.AssertExpectations(GinkgoT())
		})

		It("rejects an invalid subsystem request", func() {
			badSftp := "sftpfake"
			badSftpErrMsg := bzssh.UnauthorizedCommandError(fmt.Sprintf("'%s'", badSftp))

			doneChan := make(chan struct{})
			outboxQueue := make(chan plugin.ActionWrapper, 1)

			mockFileService := bzio.MockBzFileIo{}
			// provide the action this demo (valid) private key
			mockFileService.On("ReadFile", identityFile).Return([]byte(tests.DemoPem), nil)

			mockIoService := bzio.MockBzIo{TestData: testData}
			mockIoService.On("Write", []byte(readyMsg)).Return(len(readyMsg), nil)
			mockIoService.On("WriteErr", []byte(badSftpErrMsg)).Return(len(badSftpErrMsg), nil)

			// I guess I then need to make a client that also talks to this?
			listener, _ := net.Listen("tcp", ":22221")

			s := New(logger, outboxQueue, doneChan, identityFile, mockFileService, mockIoService, listener)

			By("starting without error")
			err := s.Start()
			Expect(err).To(BeNil())

			By("executing the SSH handshake")
			conn, err := gossh.Dial("tcp", "localhost:22221", config)
			Expect(err).To(BeNil())
			defer conn.Close()

			session, err := conn.NewSession()
			Expect(err).To(BeNil())

			By("rejecting the invalid request")
			err = session.RequestSubsystem(badSftp)
			Expect(err).To(Equal(io.EOF))

			mockFileService.AssertExpectations(GinkgoT())
			mockIoService.AssertExpectations(GinkgoT())
		})

		It("rejects all shell requests", func() {
			shellReqErrMsg := bzssh.UnauthorizedCommandError("shell request")

			doneChan := make(chan struct{})
			outboxQueue := make(chan plugin.ActionWrapper, 1)

			mockFileService := bzio.MockBzFileIo{}
			// provide the action this demo (valid) private key
			mockFileService.On("ReadFile", identityFile).Return([]byte(tests.DemoPem), nil)

			mockIoService := bzio.MockBzIo{TestData: testData}
			mockIoService.On("Write", []byte(readyMsg)).Return(len(readyMsg), nil)
			mockIoService.On("WriteErr", []byte(shellReqErrMsg)).Return(len(shellReqErrMsg), nil)

			// I guess I then need to make a client that also talks to this?
			listener, _ := net.Listen("tcp", ":22226")

			s := New(logger, outboxQueue, doneChan, identityFile, mockFileService, mockIoService, listener)

			By("starting without error")
			err := s.Start()
			Expect(err).To(BeNil())

			By("executing the SSH handshake")
			conn, err := gossh.Dial("tcp", "localhost:22226", config)
			Expect(err).To(BeNil())
			defer conn.Close()

			session, err := conn.NewSession()
			Expect(err).To(BeNil())

			By("rejecting the invalid request")
			ok, err := session.SendRequest("shell", true, []byte{})
			Expect(err).To(Equal(io.EOF))
			Expect(ok).To(BeFalse())

			mockFileService.AssertExpectations(GinkgoT())
			mockIoService.AssertExpectations(GinkgoT())
		})
	})

	// NOTE: we can't make extensive use of the hierarchy here because we're evaluating messages being passed as state changes
	Context("Happy path I: keys exist - scp - stdout - upload", func() {
		agentReply := "testAgentReply"
		channelInput := "testChannelInput"

		It("rejects an invalid exec request", func() {
			scp := "scp -f testFile.txt"

			doneChan := make(chan struct{})
			outboxQueue := make(chan plugin.ActionWrapper, 1)

			mockFileService := bzio.MockBzFileIo{}
			// provide the action this demo (valid) private key
			mockFileService.On("ReadFile", identityFile).Return([]byte(tests.DemoPem), nil)

			// we will receive a ready message upon startup
			mockIoService := bzio.MockBzIo{TestData: testData}
			mockIoService.On("Write", []byte(readyMsg)).Return(len(readyMsg), nil)

			listener, _ := net.Listen("tcp", ":22220")
			s := New(logger, outboxQueue, doneChan, identityFile, mockFileService, mockIoService, listener)

			By("starting without error")
			err := s.Start()
			Expect(err).To(BeNil())

			By("sending an open message to the agent")
			openMessage := <-outboxQueue
			Expect(openMessage.Action).To(Equal(string(bzssh.SshOpen)))
			var openPayload bzssh.SshOpenMessage
			err = json.Unmarshal(openMessage.ActionPayload, &openPayload)
			Expect(err).To(BeNil())

			By("executing the SSH handshake")
			conn, err := gossh.Dial("tcp", "localhost:22220", config)
			Expect(err).To(BeNil())
			defer conn.Close()

			session, err := conn.NewSession()
			Expect(err).To(BeNil())
			defer session.Close()

			// TODO: clean up and prove it's stdout
			stdout, err := session.StdoutPipe()
			Expect(err).To(BeNil())
			stderr, err := session.StderrPipe()
			Expect(err).To(BeNil())
			stdin, err := session.StdinPipe()
			Expect(err).To(BeNil())

			By("sending a valid exec command to the agent")
			// NOTE: don't forget that unicode codes are base-16, so to express "19" here, we use u+0013
			ok, err := session.SendRequest("exec", true, []byte(fmt.Sprintf("\u0000\u0000\u0000\u0013%s", scp)))
			Expect(err).To(BeNil())
			Expect(ok).To(BeTrue())

			execMessage := <-outboxQueue
			Expect(execMessage.Action).To(Equal(string(bzssh.SshExec)))
			var execPayload bzssh.SshExecMessage
			json.Unmarshal(execMessage.ActionPayload, &execPayload)
			Expect(execPayload.Command).To(Equal(scp))

			By("writing the agent's response to the ssh channel")

			readChan := make(chan []byte)
			go readPipe(stdout, readChan)
			go readPipe(stderr, readChan)

			messageContent := base64.StdEncoding.EncodeToString([]byte(agentReply))
			s.ReceiveStream(smsg.StreamMessage{
				Type:    smsg.StdOut,
				Content: messageContent,
				More:    true,
			})

			output := <-readChan
			Expect(string(output)).To(Equal(agentReply))

			By("sending the channel's input to the agent")
			_, err = stdin.Write([]byte(channelInput))
			Expect(err).To(BeNil())

			inputMessage := <-outboxQueue
			Expect(inputMessage.Action).To(Equal(string(bzssh.SshInput)))
			var inputPayload bzssh.SshInputMessage
			json.Unmarshal(inputMessage.ActionPayload, &inputPayload)
			Expect(string(inputPayload.Data)).To(Equal(channelInput))

			By("closing when the local ssh process ends")
			err = stdin.Close()
			Expect(err).To(BeNil())

			closeMessage := <-outboxQueue
			Expect(closeMessage.Action).To(Equal(string(bzssh.SshClose)))
			var closePayload bzssh.SshCloseMessage
			err = json.Unmarshal(closeMessage.ActionPayload, &closePayload)
			Expect(err).To(BeNil())

			mockFileService.AssertExpectations(GinkgoT())
			mockIoService.AssertExpectations(GinkgoT())
		})
	})

	Context("Happy path II: keys don't exist - sftp - stderr - download", func() {
		agentReply := "testAgentReply"

		It("rejects an invalid exec request", func() {
			sftp := "sftp"

			doneChan := make(chan struct{})
			outboxQueue := make(chan plugin.ActionWrapper, 1)

			mockFileService := bzio.MockBzFileIo{}
			// provide the action an invalid private key -- this will force it to generate a new one
			mockFileService.On("ReadFile", identityFile).Return([]byte("invalid key"), nil)
			// ...which we expect to be written out
			mockFileService.On("WriteFile", identityFile).Return(nil)

			// we will receive a ready message upon startup
			mockIoService := bzio.MockBzIo{TestData: testData}
			mockIoService.On("Write", []byte(readyMsg)).Return(len(readyMsg), nil)

			listener, _ := net.Listen("tcp", ":22225")
			s := New(logger, outboxQueue, doneChan, identityFile, mockFileService, mockIoService, listener)

			// take a few steps for granted since we already tested them
			By("starting without error")
			s.Start()
			<-outboxQueue

			By("executing the SSH handshake")
			conn, _ := gossh.Dial("tcp", "localhost:22225", config)
			defer conn.Close()
			session, _ := conn.NewSession()
			defer session.Close()

			// TODO: cleanup and prove it's stderr
			stdout, err := session.StdoutPipe()
			Expect(err).To(BeNil())
			stderr, err := session.StderrPipe()
			Expect(err).To(BeNil())

			By("sending a valid exec command to the agent")
			err = session.RequestSubsystem(sftp)
			Expect(err).To(BeNil())

			execMessage := <-outboxQueue
			Expect(execMessage.Action).To(Equal(string(bzssh.SshExec)))
			var execPayload bzssh.SshExecMessage
			json.Unmarshal(execMessage.ActionPayload, &execPayload)
			Expect(execPayload.Command).To(Equal(sftp))
			Expect(execPayload.Sftp).To(BeTrue())

			By("writing the agent's response to the ssh channel")

			readChan := make(chan []byte)
			go readPipe(stdout, readChan)
			go readPipe(stderr, readChan)

			messageContent := base64.StdEncoding.EncodeToString([]byte(agentReply))
			s.ReceiveStream(smsg.StreamMessage{
				Type:    smsg.StdErr,
				Content: messageContent,
				More:    false,
			})

			output := <-readChan
			Expect(string(output)).To(Equal(agentReply))

			By("closing when the remote command finishes")
			closeMessage := <-outboxQueue
			Expect(closeMessage.Action).To(Equal(string(bzssh.SshClose)))
			var closePayload bzssh.SshCloseMessage
			err = json.Unmarshal(closeMessage.ActionPayload, &closePayload)
			Expect(err).To(BeNil())

			mockFileService.AssertExpectations(GinkgoT())
			mockIoService.AssertExpectations(GinkgoT())
		})
	})
})

func readPipe(pipe io.Reader, outputChan chan []byte) {
	b := make([]byte, 100)
	for {
		if n, err := pipe.Read(b); err != nil {
			return
		} else if n > 0 {
			outputChan <- b[:n]
		}
	}
}
