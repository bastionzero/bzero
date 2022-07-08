package transparentssh

import (
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

func setUpServer() {
	privateKey, _, _ := bzssh.GenerateKeys()
	config := &gossh.ServerConfig{
		// TODO: is using NoClientAuth acceptable? Shows in the ssh logs as "authentication (none)", which we know is fine but may look alarming
		// however if we remove this, the ssh logs show authentication with the public key, which looks like a long-lived credential!
		NoClientAuth: true,
		PublicKeyCallback: func(c gossh.ConnMetadata, pubKey gossh.PublicKey) (*gossh.Permissions, error) {
			return &gossh.Permissions{}, nil
		},
	}
	private, _ := gossh.ParsePrivateKey(privateKey)
	config.AddHostKey(private)

	listener, _ := net.Listen("tcp", ":22232")

	go func() {
		defer listener.Close()

		// Before use, a handshake must be performed on the incoming net.Conn.
		nConn, _ := listener.Accept()
		_, chans, reqs, _ := gossh.NewServerConn(nConn, config)

		go gossh.DiscardRequests(reqs)

		go func() {
			for newChannel := range chans {
				/* don't need?
				// Channels have a type, depending on the application level protocol intended.
				if t := newChannel.ChannelType(); t != "session" {
					newChannel.Reject(gossh.UnknownChannelType, fmt.Sprintf("unknown channel type: %s", t))
					continue
				}
				*/

				channel, requests, _ := newChannel.Accept()

				// Sessions have out-of-band requests such as "shell", "pty-req" and "env"
				go func(requests <-chan *gossh.Request) {
					for req := range requests {

						switch req.Type {
						// handle scp (and someday, other exec)
						case "exec":
							// TODO: just like, report what we got?

						// handle sftp (NOTE: looks like git works over this kind of system too)
						case "subsystem":
							// TODO: just like, report what we got?

							req.Reply(true, nil)
						}
					}
				}(requests)
			}
		}()
	}()
}

func newClient(port string) *TransparentSsh {
	logger := logger.MockLogger()

	config := &gossh.ClientConfig{
		User:            "testUser",
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	}

	doneChan := make(chan struct{})
	outboxQueue := make(chan smsg.StreamMessage, 1)

	localAddr, _ := net.ResolveTCPAddr("tcp", fmt.Sprintf("localhost:%s", port))
	conn, _ := gossh.Dial("tcp", remoteAddress, config)
	t := New(logger, doneChan, outboxQueue, conn)
}

func TestDefaultSsh(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent TransparentSsh Suite")
}

var _ = Describe("Daemon TransparentSsh action", func() {

	Context("rejects unauthorized requests", func() {
		port := "22223"

	})

})
