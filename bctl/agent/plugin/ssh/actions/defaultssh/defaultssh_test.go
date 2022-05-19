package defaultssh

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	"bastionzero.com/bctl/v1/bzerolib/services/fileservice"
	"bastionzero.com/bctl/v1/bzerolib/services/tcpservice"
	"bastionzero.com/bctl/v1/bzerolib/services/userservice"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/tests"
)

func TestDefaultSsh(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent DefaultSsh Suite")
}

var _ = Describe("Agent DefaultSsh action", func() {
	logger := logger.MockLogger()
	testUser := "test-user"
	testData := "testData"

	homeDir, _ := os.MkdirTemp("", "test")
	defer os.RemoveAll(homeDir)
	keysFile := fmt.Sprintf("%s/.ssh/authorized_keys", homeDir)
	openFile, _ := os.Open(keysFile)

	go mockSshServer()
	localAddr, _ := net.ResolveTCPAddr("tcp", "localhost:2022")
	dummyConn, _ := net.DialTCP("tcp", nil, localAddr)

	Context("Happy path I: closed by user", func() {

		doneChan := make(chan struct{})
		outboxQueue := make(chan smsg.StreamMessage, 1)

		mockFileService := fileservice.MockFileService{}
		mockFileService.On("MkdirAll", fmt.Sprintf("%s/.ssh", homeDir), os.ModePerm).Return(nil)
		mockFileService.On("Open", "").Return(openFile, nil)
		mockFileService.On("Open", keysFile).Return(&os.File{}, errors.New("not going to mock os.File"))
		mockFileService.On("WriteFile", keysFile).Return(nil)
		mockFileService.On("Append", keysFile).Return(nil)

		mockTcpService := tcpservice.MockTcpService{}
		mockTcpService.On("ResolveTCPAddr", "tcp", "localhost:2022").Return(localAddr, nil)
		mockTcpService.On("DialTCP", "tcp", (*net.TCPAddr)(nil), localAddr).Return(dummyConn, nil)

		mockUserService := userservice.MockUserService{}
		mockUserService.On("Lookup", testUser).Return(&user.User{HomeDir: homeDir}, nil)

		s, err := New(logger, doneChan, outboxQueue, "localhost", "2022", testUser, mockFileService, mockTcpService, mockUserService)
		Expect(err).To(BeNil())

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

			By("passing Daemon input to SSH")
			inputMsg := ssh.SshInputMessage{
				Data: []byte(testData),
			}

			inputBytes, _ := json.Marshal(inputMsg)

			returnBytes, err = s.Receive(string(ssh.SshInput), inputBytes)
			Expect(err).To(BeNil())
			Expect(returnBytes).To(Equal([]byte{}))

			By("sending SSH output back to Daemon")
			msg := <-outboxQueue
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

func mockSshServer() {
	l, _ := net.Listen("tcp", ":2022")
	defer l.Close()
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
