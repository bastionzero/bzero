package opaquessh

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/ssh/authorizedkeys"
	"bastionzero.com/bctl/v1/bzerolib/bzio"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/tests"
)

func TestDefaultSsh(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent OpaqueSsh Suite")
}

var _ = Describe("Agent OpaqueSsh action", func() {
	logger := logger.MockLogger()
	testUser := "test-user"
	testData := "testData"
	testHostKey := "testHostKey"

	readyChan := make(chan struct{})
	go mockSshServer(readyChan)
	// wait for server to spin up
	<-readyChan

	Context("Happy path I: closed by user", func() {

		doneChan := make(chan struct{})
		outboxQueue := make(chan smsg.StreamMessage, 1)

		localAddr, _ := net.ResolveTCPAddr("tcp", "localhost:2022")
		dummyConn, _ := net.DialTCP("tcp", nil, localAddr)

		mockAuthKeyService := authorizedkeys.MockAuthorizedKey{}
		mockAuthKeyService.On("Add").Return(nil)

		mockFileService := bzio.MockBzFileIo{}
		mockFileService.On("ReadFile", rsaKeyPath).Return([]byte(testHostKey), nil)

		s := New(logger, doneChan, outboxQueue, dummyConn, mockAuthKeyService, mockFileService)

		It("relays messages between the Daemon and the local SSH process", func() {

			By("starting without error")
			openMsg := ssh.SshOpenMessage{
				TargetUser: testUser,
				PublicKey:  []byte(tests.DemoPub),
			}

			openBytes, _ := json.Marshal(openMsg)

			returnBytes, err := s.Receive(string(ssh.SshOpen), openBytes)
			Expect(err).To(BeNil())
			Expect(returnBytes).To(Equal([]byte{}))

			By("sending the host key to the daemon")
			msg := <-outboxQueue
			content, _ := base64.StdEncoding.DecodeString(msg.Content)
			Expect(msg.Type).To(Equal(smsg.Data))
			Expect(content).To(Equal([]byte(testHostKey)))

			By("passing Daemon input to SSH")
			inputMsg := ssh.SshInputMessage{
				Data: []byte(testData),
			}

			inputBytes, _ := json.Marshal(inputMsg)

			returnBytes, err = s.Receive(string(ssh.SshInput), inputBytes)
			Expect(err).To(BeNil())
			Expect(returnBytes).To(Equal([]byte{}))

			By("sending SSH output back to Daemon")
			msg = <-outboxQueue
			Expect(msg.Type).To(Equal(smsg.StdOut))
			Expect(msg.More).To(BeTrue())

			By("stopping when it receives a close message")

			closeMsg := ssh.SshCloseMessage{
				Reason: "Testing!",
			}
			closeBytes, _ := json.Marshal(closeMsg)

			returnBytes, err = s.Receive(string(ssh.SshClose), closeBytes)
			Expect(err).To(BeNil())
			Expect(returnBytes).To(Equal(closeBytes))

			_, ok := <-doneChan
			Expect(ok).To(BeFalse())
		})
	})
})

func mockSshServer(readyChan chan struct{}) {
	l, _ := net.Listen("tcp", ":2022")
	defer l.Close()
	readyChan <- struct{}{}
	buf := make([]byte, chunkSize)
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		go func() {
			_, err = conn.Read(buf)
			if err != nil {
				return
			}
		}()
		go func() {
			conn.Write(buf)
		}()
	}
}
