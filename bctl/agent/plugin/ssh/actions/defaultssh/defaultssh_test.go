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
	"bastionzero.com/bctl/v1/bzerolib/services/ioservice"
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
	homeDir := "/home/test-user"

	dummyAddr := &net.TCPAddr{}

	Context("Happy path I: closed by user", func() {

		doneChan := make(chan struct{})
		outboxQueue := make(chan smsg.StreamMessage, 1)

		mockConn := tests.MockConn{}

		mockFileService := fileservice.MockFileService{}
		mockFileService.On("Open", fmt.Sprintf("%s/.ssh/authorized_keys", homeDir)).Return(&os.File{}, errors.New("not going to mock os.File"))
		mockFileService.On("WriteFile", fmt.Sprintf("%s/.ssh/authorized_keys", homeDir)).Return(nil)

		mockIoService := ioservice.MockIoService{}

		mockTcpService := tcpservice.MockTcpService{}
		mockTcpService.On("ResolveTCPAddr", "tcp", "localhost:22").Return(dummyAddr, nil)
		mockTcpService.On("DialTCP", "tcp", nil, dummyAddr).Return(mockConn, nil)

		mockUserService := userservice.MockUserService{}
		mockUserService.On("Lookup", testUser).Return(&user.User{HomeDir: homeDir}, nil)

		s, err := New(logger, doneChan, outboxQueue, testUser, mockFileService, mockIoService, mockTcpService, mockUserService)
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
			Expect(msg.Action).To(Equal(smsg.StdOut))
			Expect(msg.More).To(BeTrue())

			By("stopping when it receives a close message")

			closeMsg := ssh.SshCloseMessage{}
			closeBytes, _ := json.Marshal(closeMsg)

			returnBytes, err = s.Receive(string(ssh.SshClose), closeBytes)
			Expect(err).To(BeNil())
			Expect(returnBytes).To(Equal([]byte{}))

			receivedStopMsg := false
			// need to drain the channel, as it has been reading continuously
			for len(outboxQueue) > 0 {
				msg := <-outboxQueue
				if msg.Action == string(smsg.StdOut) && !msg.More {
					receivedStopMsg = true
				}
			}
			Expect(receivedStopMsg).To(BeTrue())

			_, ok := <-doneChan
			Expect(ok).To(BeFalse())
		})
	})

	// TODO: happy path II: closed by error

})
