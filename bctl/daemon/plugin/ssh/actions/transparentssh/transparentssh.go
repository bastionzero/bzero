package transparentssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"

	gossh "golang.org/x/crypto/ssh"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/bzio"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	InputBufferSize = int(64 * 1024)
	endedByUser     = "SSH session ended"
)

const readyMsg = "BZERO-DAEMON READY-TO-CONNECT"

type TransparentSsh struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	outboxQueue chan plugin.ActionWrapper
	doneChan    chan struct{}

	identityFile string

	filIo bzio.BzFileIo
	stdIo bzio.BzIo

	// used to communicate directly with the SSH process
	sshListener net.Listener
	sshChannel  io.ReadWriteCloser
}

func New(
	logger *logger.Logger,
	outboxQueue chan plugin.ActionWrapper,
	doneChan chan struct{},
	identityFile string,
	filIo bzio.BzFileIo,
	stdIo bzio.BzIo,
	listener net.Listener,
) *TransparentSsh {

	return &TransparentSsh{
		logger:       logger,
		outboxQueue:  outboxQueue,
		doneChan:     doneChan,
		identityFile: identityFile,
		filIo:        filIo,
		stdIo:        stdIo,
		sshListener:  listener,
	}
}

func (t *TransparentSsh) Done() <-chan struct{} {
	return t.doneChan
}

func (t *TransparentSsh) Kill() {
	if t.sshChannel != nil {
		t.sshChannel.Close()
	}
	t.tmb.Kill(nil)
}

func (t *TransparentSsh) Start() error {

	var privateKey []byte
	// FIXME: stopgap so that there is always an identityfile
	if publicKeyRsa, err := bzssh.ReadPublicKeyRsa(t.identityFile, t.filIo); err == nil {
		if _, err = bzssh.GeneratePublicKey(publicKeyRsa); err != nil {
			return fmt.Errorf("error decoding temporary public key: %s", err)
		} else {
			t.logger.Debugf("using existing temporary keys")
		}
	} else {
		t.logger.Debugf("generating new temporary keys")
		privateKey, _, err = bzssh.GenerateKeys()
		if err != nil {
			return fmt.Errorf("error generating temporary keys: %s", err)
		} else if err := t.filIo.WriteFile(t.identityFile, privateKey, 0600); err != nil {
			return fmt.Errorf("error writing temporary private key: %s", err)
		}
	}

	sshOpenMessage := bzssh.SshOpenMessage{}

	t.sendOutputMessage(bzssh.SshOpen, sshOpenMessage)

	go func() {
		defer close(t.doneChan)
		<-t.tmb.Dying()
	}()

	// the following implementation of an ssh server is based heavily on this example:
	// https://github.com/Scalingo/go-ssh-examples/blob/master/server_complex.go

	// An SSH server is represented by a ServerConfig, which holds
	// certificate details and handles authentication of ServerConns.
	config := &gossh.ServerConfig{
		NoClientAuth: true, // TODO: is this okay? Shows in the logs as "authentication (none)", which we know is fine but may look alarming
		PublicKeyCallback: func(c gossh.ConnMetadata, pubKey gossh.PublicKey) (*gossh.Permissions, error) {
			return &gossh.Permissions{}, nil
		},
	}

	newPrivate, _, _ := bzssh.GenerateKeys()
	private, _ := gossh.ParsePrivateKey(newPrivate)
	config.AddHostKey(private)
	go func() {
		defer t.sshListener.Close()

		// Once a ServerConfig has been configured, tell ZLI we can accept connections
		t.stdIo.Write([]byte(readyMsg))

		nConn, _ := t.sshListener.Accept()
		// Before use, a handshake must be performed on the incoming net.Conn.
		_, chans, reqs, err := gossh.NewServerConn(nConn, config)

		if err != nil {
			t.logger.Errorf("failed to handshake: ", err)
		}

		go gossh.DiscardRequests(reqs)

		go func() {
			for newChannel := range chans {
				// Channels have a type, depending on the application level
				// protocol intended. In the case of a shell, the type is
				// "session" and ServerShell may be used to present a simple
				// terminal interface.
				if t := newChannel.ChannelType(); t != "session" {
					newChannel.Reject(gossh.UnknownChannelType, fmt.Sprintf("unknown channel type: %s", t))
					continue
				}
				channel, requests, err := newChannel.Accept()
				t.sshChannel = channel
				if err != nil {
					t.logger.Errorf("could not accept channel (%s)", err)
					continue
				}

				// Sessions have out-of-band requests such as "shell", "pty-req" and "env"
				go func(in <-chan *gossh.Request) {
					for req := range in {
						ok := false
						switch req.Type {
						// handle scp (and someday, other exec)
						case "exec":
							command := string(req.Payload[4 : req.Payload[3]+4])
							if !bzssh.IsValidScp(command) {
								errMsg := bzssh.UnauthorizedCommandError(fmt.Sprintf("'%s'", command))
								t.logger.Errorf(errMsg)
								t.stdIo.WriteErr([]byte(errMsg))
								t.Kill()
								return
							}

							ok = true
							go t.readFromChannel()

							sshExecMessage := bzssh.SshExecMessage{
								Command: command,
							}
							t.sendOutputMessage(bzssh.SshExec, sshExecMessage)

						// handle sftp (looks like git works over this kind of system too)
						case "subsystem":
							command := string(req.Payload[4 : req.Payload[3]+4])

							if !bzssh.IsValidSftp(command) {
								errMsg := bzssh.UnauthorizedCommandError(fmt.Sprintf("'%s'", command))
								t.logger.Errorf(errMsg)
								t.stdIo.WriteErr([]byte(errMsg))
								t.Kill()
								return
							}

							ok = true
							go t.readFromChannel()

							sshExecMessage := bzssh.SshExecMessage{
								Command: command,
								Sftp:    true,
							}
							t.sendOutputMessage(bzssh.SshExec, sshExecMessage)

						case "shell":
							errMsg := bzssh.UnauthorizedCommandError("shell request")
							t.logger.Errorf(errMsg)
							t.stdIo.WriteErr([]byte(errMsg))
							t.Kill()

						case "pty-req":
							errMsg := bzssh.UnauthorizedCommandError("PTY request")
							t.logger.Errorf(errMsg)
							t.stdIo.WriteErr([]byte(errMsg))
							t.Kill()
						}

						if !ok {
							t.logger.Errorf("declining %s request", req.Type)
						}

						req.Reply(ok, nil)
					}
				}(requests)
			}
		}()
	}()

	return nil
}

// send anything we get from local SSH up to the agent
func (t *TransparentSsh) readFromChannel() {

	b := make([]byte, InputBufferSize)

	for {
		select {
		case <-t.tmb.Dying():
			return
		default:
			n, err := t.sshChannel.Read(b)
			if err != nil {
				if err == io.EOF {
					t.sendOutputMessage(bzssh.SshClose, bzssh.SshCloseMessage{Reason: endedByUser})
					t.logger.Errorf("finished reading from stdin")
					return
				} else {
					t.sendOutputMessage(bzssh.SshClose, bzssh.SshCloseMessage{Reason: err.Error()})
					t.logger.Errorf("error reading from Stdin: %s", err)
					return
				}
			} else if n > 0 {
				t.logger.Debugf("Sending %d bytes to remote SSH", n)
				t.sendOutputMessage(bzssh.SshInput, bzssh.SshInputMessage{Data: b[:n]})
			}
		}
	}
}

func (t *TransparentSsh) ReceiveStream(smessage smsg.StreamMessage) {
	switch smsg.StreamType(smessage.Type) {
	// TODO: we don't expect any stderr messages to come -- treat them the same as stdout?
	case smsg.StdErr:
		t.logger.Errorf("received bytes from remote stderr")
		fallthrough
	case smsg.StdOut:
		if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
			t.logger.Errorf("Error decoding ssh StdOut stream content: %s", err)
		} else {
			t.logger.Infof("sending %d bytes to channel", len(contentBytes))
			if _, err = t.sshChannel.Write(contentBytes); err != nil {
				t.logger.Errorf("Error writing to Stdout: %s", err)
			}
			if !smessage.More {
				t.logger.Errorf("received ssh close stream message")
				t.Kill()
			}
		}
	case smsg.Error:
		// let the ZLI know if the agent encountered a policy error
		t.logger.Errorf("received an error from the agent")
		if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
			t.logger.Errorf("error decoding ssh StdOut stream content: %s", err)
		} else {
			t.stdIo.WriteErr([]byte(contentBytes))
		}
		t.Kill()
		return
	case smsg.Stop:
		t.logger.Infof("received stop message from agent. Shutting down...")
		t.Kill()
	default:
		t.logger.Errorf("unhandled stream type: %s", smessage.Type)
	}
}

func (t *TransparentSsh) sendOutputMessage(action bzssh.SshSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	t.outboxQueue <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}
}
