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

func startSession(t *TransparentSsh, port string, config *gossh.ClientConfig) (*gossh.Client, *gossh.Session) {
	By("starting without error")
	err := t.Start()
	Expect(err).To(BeNil())

	By("executing the SSH handshake")
	conn, err := gossh.Dial("tcp", fmt.Sprintf("localhost:%s", port), config)
	Expect(err).To(BeNil())

	session, err := conn.NewSession()
	Expect(err).To(BeNil())

	return conn, session
}

// provide pipes for two-way communication with the server
func setupIo(session *gossh.Session) (io.WriteCloser, chan []byte, chan []byte) {
	stdout, err := session.StdoutPipe()
	Expect(err).To(BeNil())
	stderr, err := session.StderrPipe()
	Expect(err).To(BeNil())
	stdin, err := session.StdinPipe()
	Expect(err).To(BeNil())

	stdoutChan := make(chan []byte)
	stderrChan := make(chan []byte)

	go readPipe(stdout, stdoutChan)
	go readPipe(stderr, stderrChan)

	return stdin, stdoutChan, stderrChan
}

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

func safeListen(port string) net.Listener {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	Expect(err).To(BeNil())
	return listener
}

func TestDefaultSsh(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Daemon TransparentSsh Suite")
}

var _ = Describe("Daemon TransparentSsh action", func() {
	logger := logger.MockLogger()
	identityFilePath := "testIdFile"
	knownHostsFilePath := "testKhFile"
	testData := "testData"

	var config *gossh.ClientConfig

	BeforeEach(func() {
		privateBytes, _, err := bzssh.GenerateKeys()
		Expect(err).To(BeNil())
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

		var conn *gossh.Client
		var session *gossh.Session

		AfterEach(func() {
			conn.Close()
			session.Close()
		})

		It("rejects an invalid exec request", func() {
			badScp := "scpfake"
			port := "22220"
			badScpErrMsg := bzssh.UnauthorizedCommandError(fmt.Sprintf("'%s'", badScp))

			doneChan := make(chan struct{})
			outboxQueue := make(chan plugin.ActionWrapper, 1)

			mockFileService := bzio.MockBzFileIo{}
			// provide the action this demo (valid) private key
			mockFileService.On("ReadFile", identityFilePath).Return([]byte(tests.DemoPem), nil)
			idFile := bzssh.NewIdentityFile(identityFilePath, mockFileService)
			// also expect a new entry in known_hosts
			mockFileService.On("WriteFile", knownHostsFilePath).Return(nil)
			khFile := bzssh.NewKnownHosts(knownHostsFilePath, []string{"testHost"}, mockFileService)

			mockIoService := bzio.MockBzIo{TestData: testData}
			mockIoService.On("Write", []byte(readyMsg)).Return(len(readyMsg), nil)
			mockIoService.On("WriteErr", []byte(badScpErrMsg)).Return(len(badScpErrMsg), nil)

			listener := safeListen(port)
			t := New(logger, outboxQueue, doneChan, mockIoService, listener, idFile, khFile)
			conn, session = startSession(t, port, config)

			By("rejecting the invalid request")
			ok, err := session.SendRequest("exec", true, []byte(fmt.Sprintf("\u0000\u0000\u0000\u0007%s", badScp)))
			Expect(err).To(Equal(io.EOF))
			Expect(ok).To(BeFalse())

			mockFileService.AssertExpectations(GinkgoT())
			mockIoService.AssertExpectations(GinkgoT())
		})

		It("rejects an invalid subsystem request", func() {
			badSftp := "sftpfake"
			port := "22221"
			badSftpErrMsg := bzssh.UnauthorizedCommandError(fmt.Sprintf("'%s'", badSftp))

			doneChan := make(chan struct{})
			outboxQueue := make(chan plugin.ActionWrapper, 1)

			mockFileService := bzio.MockBzFileIo{}
			// provide the action this demo (valid) private key
			mockFileService.On("ReadFile", identityFilePath).Return([]byte(tests.DemoPem), nil)
			idFile := bzssh.NewIdentityFile(identityFilePath, mockFileService)
			// also expect a new entry in known_hosts
			mockFileService.On("WriteFile", knownHostsFilePath).Return(nil)
			khFile := bzssh.NewKnownHosts(knownHostsFilePath, []string{"testHost"}, mockFileService)

			mockIoService := bzio.MockBzIo{TestData: testData}
			mockIoService.On("Write", []byte(readyMsg)).Return(len(readyMsg), nil)
			mockIoService.On("WriteErr", []byte(badSftpErrMsg)).Return(len(badSftpErrMsg), nil)

			listener := safeListen(port)
			t := New(logger, outboxQueue, doneChan, mockIoService, listener, idFile, khFile)
			conn, session = startSession(t, port, config)

			By("rejecting the invalid request")
			err := session.RequestSubsystem(badSftp)
			Expect(err).To(Equal(io.EOF))

			mockFileService.AssertExpectations(GinkgoT())
			mockIoService.AssertExpectations(GinkgoT())
		})

		It("rejects all shell requests", func() {
			port := "22222"
			shellReqErrMsg := bzssh.UnauthorizedCommandError("shell request")

			doneChan := make(chan struct{})
			outboxQueue := make(chan plugin.ActionWrapper, 1)

			mockFileService := bzio.MockBzFileIo{}
			// provide the action this demo (valid) private key
			mockFileService.On("ReadFile", identityFilePath).Return([]byte(tests.DemoPem), nil)
			idFile := bzssh.NewIdentityFile(identityFilePath, mockFileService)
			// also expect a new entry in known_hosts
			mockFileService.On("WriteFile", knownHostsFilePath).Return(nil)
			khFile := bzssh.NewKnownHosts(knownHostsFilePath, []string{"testHost"}, mockFileService)

			mockIoService := bzio.MockBzIo{TestData: testData}
			mockIoService.On("Write", []byte(readyMsg)).Return(len(readyMsg), nil)
			mockIoService.On("WriteErr", []byte(shellReqErrMsg)).Return(len(shellReqErrMsg), nil)

			listener := safeListen(port)
			t := New(logger, outboxQueue, doneChan, mockIoService, listener, idFile, khFile)
			conn, session = startSession(t, port, config)

			By("rejecting the invalid request")
			ok, err := session.SendRequest("shell", true, []byte("\u0000\u0000\u0000\u000exterm-256color"))
			Expect(err).To(Equal(io.EOF))
			Expect(ok).To(BeFalse())

			mockFileService.AssertExpectations(GinkgoT())
			mockIoService.AssertExpectations(GinkgoT())
		})
	})

	Context("Happy path I: keys exist - scp - stdout - upload", func() {
		agentReply := "testAgentReply"
		channelInput := "testChannelInput"

		var conn *gossh.Client
		var session *gossh.Session

		AfterEach(func() {
			conn.Close()
			session.Close()
		})

		It("handles the request from start to finish", func() {
			scp := "scp -t testFile.txt"
			port := "22223"

			doneChan := make(chan struct{})
			outboxQueue := make(chan plugin.ActionWrapper, 1)

			mockFileService := bzio.MockBzFileIo{}
			// provide the action this demo (valid) private key
			mockFileService.On("ReadFile", identityFilePath).Return([]byte(tests.DemoPem), nil)
			idFile := bzssh.NewIdentityFile(identityFilePath, mockFileService)
			// also expect a new entry in known_hosts
			mockFileService.On("WriteFile", knownHostsFilePath).Return(nil)
			khFile := bzssh.NewKnownHosts(knownHostsFilePath, []string{"testHost"}, mockFileService)

			mockIoService := bzio.MockBzIo{TestData: testData}
			mockIoService.On("Write", []byte(readyMsg)).Return(len(readyMsg), nil)

			listener := safeListen(port)
			t := New(logger, outboxQueue, doneChan, mockIoService, listener, idFile, khFile)
			conn, session = startSession(t, port, config)

			By("sending an open message to the agent")
			openMessage := <-outboxQueue
			Expect(openMessage.Action).To(Equal(string(bzssh.SshOpen)))
			var openPayload bzssh.SshOpenMessage
			err := json.Unmarshal(openMessage.ActionPayload, &openPayload)
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

			By("writing the agent's response to the ssh channel's stdout")
			messageContent := base64.StdEncoding.EncodeToString([]byte(agentReply))
			t.ReceiveStream(smsg.StreamMessage{
				Type:    smsg.StdOut,
				Content: messageContent,
				More:    true,
			})

			stdin, stdoutChan, _ := setupIo(session)
			output := <-stdoutChan
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

		var conn *gossh.Client
		var session *gossh.Session

		AfterEach(func() {
			conn.Close()
			session.Close()
		})

		It("handles the request from start to finish", func() {
			sftp := "sftp"
			port := "22224"

			doneChan := make(chan struct{})
			outboxQueue := make(chan plugin.ActionWrapper, 1)

			mockFileService := bzio.MockBzFileIo{}
			// provide the action an invalid private key -- this will force it to generate a new one...
			mockFileService.On("ReadFile", identityFilePath).Return([]byte("invalid key"), nil)
			// ...which we expect to be written out
			mockFileService.On("WriteFile", identityFilePath).Return(nil)
			idFile := bzssh.NewIdentityFile(identityFilePath, mockFileService)
			// also expect a new entry in known_hosts
			mockFileService.On("WriteFile", knownHostsFilePath).Return(nil)
			khFile := bzssh.NewKnownHosts(knownHostsFilePath, []string{"testHost"}, mockFileService)

			// we will receive a ready message upon startup
			mockIoService := bzio.MockBzIo{TestData: testData}
			mockIoService.On("Write", []byte(readyMsg)).Return(len(readyMsg), nil)

			listener := safeListen(port)
			t := New(logger, outboxQueue, doneChan, mockIoService, listener, idFile, khFile)
			conn, session = startSession(t, port, config)

			// take the open message for granted since we already tested
			<-outboxQueue

			By("sending a valid exec command to the agent")
			err := session.RequestSubsystem(sftp)
			Expect(err).To(BeNil())

			execMessage := <-outboxQueue
			Expect(execMessage.Action).To(Equal(string(bzssh.SshExec)))
			var execPayload bzssh.SshExecMessage
			json.Unmarshal(execMessage.ActionPayload, &execPayload)
			Expect(execPayload.Command).To(Equal(sftp))
			Expect(execPayload.Sftp).To(BeTrue())

			By("writing the agent's response to the ssh channel's stderr")
			messageContent := base64.StdEncoding.EncodeToString([]byte(agentReply))
			t.ReceiveStream(smsg.StreamMessage{
				Type:    smsg.StdErr,
				Content: messageContent,
				More:    false,
			})

			_, _, stderrChan := setupIo(session)
			output := <-stderrChan
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
