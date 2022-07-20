package transparentssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gossh "golang.org/x/crypto/ssh"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

var (
	execWasCalled      bool = false
	subsystemWasCalled bool = false
)

func startServer(port string) (chan gossh.Channel, chan []byte) {
	privateKey, _, _ := bzssh.GenerateKeys()
	config := &gossh.ServerConfig{
		NoClientAuth: true,
		PublicKeyCallback: func(c gossh.ConnMetadata, pubKey gossh.PublicKey) (*gossh.Permissions, error) {
			return &gossh.Permissions{}, nil
		},
	}
	private, _ := gossh.ParsePrivateKey(privateKey)
	config.AddHostKey(private)

	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	Expect(err).To(BeNil())
	channelChan := make(chan gossh.Channel)
	dataChan := make(chan []byte)

	go func() {
		defer listener.Close()

		// Before use, a handshake must be performed on the incoming net.Conn.
		nConn, err := listener.Accept()
		Expect(err).To(BeNil())
		_, chans, reqs, err := gossh.NewServerConn(nConn, config)
		Expect(err).To(BeNil())

		go gossh.DiscardRequests(reqs)

		go func() {
			for newChannel := range chans {
				channel, requests, _ := newChannel.Accept()
				go func(requests <-chan *gossh.Request) {
					for req := range requests {
						payloadSize := int(req.Payload[3])
						command := req.Payload[4 : 4+payloadSize]

						switch req.Type {
						case "exec":
							execWasCalled = true
							channelChan <- channel
							dataChan <- command

						case "subsystem":
							subsystemWasCalled = true
							channelChan <- channel
							dataChan <- command

							req.Reply(true, nil)
						}
					}
				}(requests)
			}
		}()
	}()

	return channelChan, dataChan
}

func newClient(port string) (*TransparentSsh, chan struct{}, chan smsg.StreamMessage) {
	logger := logger.MockLogger()

	config := &gossh.ClientConfig{
		User:            "testUser",
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	}

	doneChan := make(chan struct{})
	outboxQueue := make(chan smsg.StreamMessage, 1)

	conn, _ := gossh.Dial("tcp", fmt.Sprintf("localhost:%s", port), config)
	return New(logger, doneChan, outboxQueue, conn), doneChan, outboxQueue
}

func readStdin(channel gossh.Channel, outputChan chan []byte) {
	b := make([]byte, 100)
	for {
		if n, err := channel.Read(b); err != nil {
			return
		} else if n > 0 {
			outputChan <- b[:n]
		}
	}
}

func TestDefaultSsh(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent TransparentSsh Suite")
}

var _ = Describe("Daemon TransparentSsh action", func() {

	openBytes, _ := json.Marshal(bzssh.SshOpenMessage{})
	var t *TransparentSsh

	AfterEach(func() {
		t.Kill()
	})

	Context("rejects unauthorized requests", func() {
		It("rejects an invalid exec request", func() {
			port := "22230"
			badScp := "scpfake"

			startServer(port)
			t, _, _ = newClient(port)

			// we can chck this explicitly in a different test
			t.Receive(string(bzssh.SshOpen), openBytes)

			By("returning an unauthorized command error")
			execBytes, _ := json.Marshal(bzssh.SshExecMessage{Command: badScp})
			result, err := t.Receive(string(bzssh.SshExec), execBytes)
			Expect(result).To(BeNil())
			Expect(err.Error()).To(Equal(bzssh.UnauthorizedCommandError(fmt.Sprintf("'%s'", badScp))))
		})

		It("rejects an invalid sftp request", func() {
			port := "22231"
			badSftp := "sftpfake"

			startServer(port)
			t, _, _ = newClient(port)

			// we can chck this explicitly in a different test
			t.Receive(string(bzssh.SshOpen), openBytes)

			By("returning an unauthorized command error")
			execBytes, _ := json.Marshal(bzssh.SshExecMessage{Command: badSftp, Sftp: true})
			result, err := t.Receive(string(bzssh.SshExec), execBytes)
			Expect(result).To(BeNil())
			Expect(err.Error()).To(Equal(bzssh.UnauthorizedCommandError(fmt.Sprintf("'%s'", badSftp))))
		})
	})

	Context("Happy path I: scp - stderr - download ", func() {
		It("handles the request from start to finish", func() {
			port := "22232"
			scp := "scp -f testFile.txt"
			sshReply := "sshReply"
			daemonReply := "daemonReply"

			channelChan, dataChan := startServer(port)
			t, _, outboxQueue := newClient(port)

			By("starting without error")
			openBytes, _ := json.Marshal(bzssh.SshOpenMessage{})
			result, err := t.Receive(string(bzssh.SshOpen), openBytes)
			Expect(result).To(Equal([]byte{}))
			Expect(err).To(BeNil())

			By("forwarding a valid exec request to the remote ssh process")
			execBytes, _ := json.Marshal(bzssh.SshExecMessage{Command: scp})
			// need to do this in a goroutine so we can monitor it in realtime
			go t.Receive(string(bzssh.SshExec), execBytes)

			sshChannel := <-channelChan
			sentData := <-dataChan
			Expect(string(sentData)).To(Equal(scp))
			Expect(execWasCalled).To(BeTrue())

			By("communicating stderr messages back to the daemon")
			sshChannel.Stderr().Write([]byte(sshReply))
			msg := <-outboxQueue
			Expect(msg.Type).To(Equal(smsg.StdErr))
			Expect(msg.More).To(BeTrue())
			content, _ := base64.StdEncoding.DecodeString(msg.Content)
			Expect(string(content)).To(Equal(sshReply))

			By("writing incoming daemon messages to stdin")
			stdinChan := make(chan []byte)
			go readStdin(sshChannel, stdinChan)

			inputBytes, _ := json.Marshal(bzssh.SshInputMessage{Data: []byte(daemonReply)})
			t.Receive(string(bzssh.SshInput), inputBytes)
			stdinReceived := <-stdinChan
			Expect(string(stdinReceived)).To(Equal(daemonReply))

			By("shutting down when the remote ssh process ends")
			sshChannel.Close()
			msg = <-outboxQueue
			// could be from stdout or stderr, so we don't check that
			Expect(msg.More).To(BeFalse())
		})
	})

	Context("Happy path II: sftp - stdout - upload ", func() {
		It("handles the request from start to finish", func() {
			port := "22233"
			sftp := "sftp"
			sshReply := "sshReply"

			channelChan, dataChan := startServer(port)
			t, _, outboxQueue := newClient(port)

			// take for granted because tested elsewhere
			openBytes, _ := json.Marshal(bzssh.SshOpenMessage{})
			t.Receive(string(bzssh.SshOpen), openBytes)

			By("forwarding a valid subsystem request to the remote ssh process")
			execBytes, _ := json.Marshal(bzssh.SshExecMessage{Command: sftp, Sftp: true})
			// need to do this in a goroutine so we can monitor it in realtime
			go t.Receive(string(bzssh.SshExec), execBytes)

			sshChannel := <-channelChan
			sentData := <-dataChan
			Expect(string(sentData)).To(Equal(sftp))
			Expect(subsystemWasCalled).To(BeTrue())

			By("communicating stdout messages back to the daemon")
			sshChannel.Write([]byte(sshReply))
			msg := <-outboxQueue
			Expect(msg.Type).To(Equal(smsg.StdOut))
			Expect(msg.More).To(BeTrue())
			content, _ := base64.StdEncoding.DecodeString(msg.Content)
			Expect(string(content)).To(Equal(sshReply))

			By("shutting down when the daemon says its done")
			closeBytes, _ := json.Marshal(bzssh.SshCloseMessage{})
			go t.Receive(string(bzssh.SshClose), closeBytes)

			msg = <-outboxQueue
			Expect(msg.Type).To(Equal(smsg.Stop))
			Expect(msg.More).To(BeFalse())
		})
	})
})
