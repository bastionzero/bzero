package transparentssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"

	gossh "golang.org/x/crypto/ssh"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/ssh/actions/opaquessh"
	"bastionzero.com/bctl/v1/bzerolib/bzio"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

var (
	DEFAULT_SHELL string = "sh"
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

	// channel where we push from StdIn
	// FIXME: not quite true
	stdInChan chan []byte

	identityFile string

	filIo bzio.BzFileIo
	stdIo io.ReadWriter

	sshChannel io.ReadWriteCloser
}

func New(
	logger *logger.Logger,
	outboxQueue chan plugin.ActionWrapper,
	doneChan chan struct{},
	identityFile string,
	filIo bzio.BzFileIo,
	stdIo io.ReadWriter,
) *TransparentSsh {

	return &TransparentSsh{
		logger:       logger,
		outboxQueue:  outboxQueue,
		doneChan:     doneChan,
		stdInChan:    make(chan []byte),
		identityFile: identityFile,
		filIo:        filIo,
		stdIo:        stdIo,
	}
}

func (t *TransparentSsh) Done() <-chan struct{} {
	return t.doneChan
}

func (t *TransparentSsh) Kill() {
	t.tmb.Kill(nil)
}

func (t *TransparentSsh) Start() error {

	var privateKey []byte
	// FIXME: stopgap so that there is always an identityfile
	if publicKeyRsa, err := opaquessh.ReadPublicKeyRsa(t.identityFile, t.filIo); err == nil {
		if _, err = opaquessh.GeneratePublicKey(publicKeyRsa); err != nil {
			return fmt.Errorf("error decoding temporary public key: %s", err)
		} else {
			t.logger.Debugf("using existing temporary keys")
		}
	} else {
		t.logger.Debugf("generating new temporary keys")
		privateKey, _, err = opaquessh.GenerateKeys()
		if err != nil {
			return fmt.Errorf("error generating temporary keys: %s", err)
		} else if err := t.filIo.WriteFile(t.identityFile, privateKey, 0600); err != nil {
			return fmt.Errorf("error writing temporary private key: %s", err)
		}
	}

	sshOpenMessage := ssh.SshOpenMessage{}

	t.sendOutputMessage(ssh.SshOpen, sshOpenMessage)

	go func() {
		defer close(t.doneChan)
		<-t.tmb.Dying()
	}()

	// the following implementation of an ssh server is based heavily on this example:
	// https://github.com/Scalingo/go-ssh-examples/blob/master/server_complex.go

	// An SSH server is represented by a ServerConfig, which holds
	// certificate details and handles authentication of ServerConns.
	config := &gossh.ServerConfig{
		// don't even think this part is strictly necessary...
		PublicKeyCallback: func(c gossh.ConnMetadata, pubKey gossh.PublicKey) (*gossh.Permissions, error) {
			return &gossh.Permissions{
				// Record the public key used for authentication.
				Extensions: map[string]string{
					"pubkey-fp": gossh.FingerprintSHA256(pubKey),
				},
			}, nil
		},
	}

	newPrivate, _, _ := opaquessh.GenerateKeys()
	private, _ := gossh.ParsePrivateKey(newPrivate)
	config.AddHostKey(private)
	go func() {
		// Once a ServerConfig has been configured, tell ZLI we can accept connections
		listener, err := net.Listen("tcp", ":2222")
		if err != nil {
			t.logger.Errorf("failed to listen for connection: ", err)
		}
		t.stdIo.Write([]byte(readyMsg))

		defer listener.Close()

		nConn, _ := listener.Accept()
		// Before use, a handshake must be performed on the incoming
		// net.Conn.
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
						t.logger.Errorf("What I am is %s and what I have is %s", req.Type, req.Payload)
						ok := false
						switch req.Type {
						// handles scp and other exec
						case "exec":
							command := string(req.Payload[4 : req.Payload[3]+4])
							// TODO: read the command; if invalid, tell the ZLI that
							t.logger.Infof("Lucie, the command is %s", command)
							if !ssh.IsValidScp(command) {
								t.logger.Errorf("invalid command: this user is only allowed to perform file upload / download via scp, but recieved %s", command)
								channel.Close()
								t.Kill()
								return
							}

							ok = true

							go t.readFromChannel()

							sshExecMessage := ssh.SshExecMessage{
								Command: command,
							}
							t.sendOutputMessage(ssh.SshExec, sshExecMessage)

						case "shell":
							// TODO: make sure we properly reject this for now

						case "pty-req":
							// TODO: make sure we properly reject this for now

						case "window-change":
							// TODO: make sure we properly reject this for now
						}

						if !ok {
							t.logger.Errorf("declining %s request...", req.Type)
						}

						t.logger.Infof("Replying %v", ok)

						req.Reply(ok, nil)
					}
				}(requests)
			}
		}()
	}()

	return nil
}

func (t *TransparentSsh) readFromChannel() {

	b := make([]byte, InputBufferSize)
	t.logger.Errorf("Trying to read from stdin...")

	for {
		select {
		case <-t.tmb.Dying():
			t.sshChannel.Close()
			return
		default:
			n, err := t.sshChannel.Read(b)
			if err != nil {
				if err == io.EOF {
					t.sshChannel.Close()
					t.sendOutputMessage(ssh.SshClose, ssh.SshCloseMessage{Reason: endedByUser})
					t.logger.Errorf("finished reading from stdin")
					return
				} else {
					t.sshChannel.Close()
					t.logger.Errorf("error reading from Stdin: %s", err)
					return
				}
			} else if n > 0 {
				t.logger.Errorf("Here they are, %s", b[:n])
				t.sendOutputMessage(ssh.SshInput, ssh.SshInputMessage{Data: b[:n]})
			} else {
				t.logger.Errorf("Read but channel didn't give me anything")
			}
		}
	}
}

func (t *TransparentSsh) ReceiveStream(smessage smsg.StreamMessage) {
	t.logger.Debugf("Default ssh received %+v stream", smessage.Type)
	switch smsg.StreamType(smessage.Type) {
	case smsg.Data:
		if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
			t.logger.Errorf("Error decoding ssh Data stream content: %s", err)
		} else {
			if _, err = t.sshChannel.Write(contentBytes); err != nil {
				t.logger.Errorf("Error writing to Stdout: %s", err)
			}
			if !smessage.More {
				t.tmb.Kill(fmt.Errorf("received ssh close stream message"))
				t.sshChannel.Close()
				return
			}
		}
	// FIXME: just a stopgap
	case smsg.StdErr:
		fallthrough
	case smsg.StdOut:
		if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
			t.logger.Errorf("Error decoding ssh StdOut stream content: %s", err)
		} else {
			t.logger.Infof("sending %d bytes to channel: %v", len(contentBytes), contentBytes)
			//t.stdInChan <- contentBytes
			t.logger.Infof("Knock knock?")
			if _, err = t.sshChannel.Write(contentBytes); err != nil {
				t.logger.Errorf("Error writing to Stdout: %s", err)
			}
			if !smessage.More {
				t.tmb.Kill(fmt.Errorf("received ssh close stream message"))
				t.sshChannel.Close()
			}
		}
	case smsg.Error:
		t.tmb.Kill(fmt.Errorf("received an error from the agent"))
		t.sshChannel.Close()
		return
	default:
		t.logger.Errorf("unhandled stream type: %s", smessage.Type)
	}
}

func (t *TransparentSsh) sendOutputMessage(action ssh.SshSubAction, payload interface{}) {
	// Send payload to plugin output queue
	t.logger.Infof("Sending %s message", action)
	payloadBytes, _ := json.Marshal(payload)
	t.outboxQueue <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}
}
