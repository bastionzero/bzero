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
	InputBufferSize  = int(64 * 1024)
	endedByUser      = "SSH session ended"
	sshPayloadOffset = 4
)

const readyMsg = "BZERO-DAEMON READY-TO-CONNECT"

type TransparentSsh struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	outboxQueue chan plugin.ActionWrapper
	doneChan    chan struct{}

	// used to communicate directly with the ZLI via stdio
	zliIo bzio.BzIo
	// used to communicate directly with the SSH process via TCP
	sshListener net.Listener
	sshChannel  gossh.Channel

	identityFile bzssh.IIdentityFile
	knownHosts   bzssh.IKnownHosts
}

func New(
	logger *logger.Logger,
	outboxQueue chan plugin.ActionWrapper,
	doneChan chan struct{},
	zliIo bzio.BzIo,
	listener net.Listener,
	identityFile bzssh.IIdentityFile,
	knownHosts bzssh.IKnownHosts,
) *TransparentSsh {

	return &TransparentSsh{
		logger:       logger,
		outboxQueue:  outboxQueue,
		doneChan:     doneChan,
		zliIo:        zliIo,
		sshListener:  listener,
		identityFile: identityFile,
		knownHosts:   knownHosts,
	}
}

func (t *TransparentSsh) Done() <-chan struct{} {
	return t.doneChan
}

// internal pre-kill function on success
func (t *TransparentSsh) signalSuccess() {
	if t.sshChannel != nil {
		t.sshChannel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
	}
}

func (t *TransparentSsh) Kill() {
	if t.sshChannel != nil {
		t.sshChannel.Close()
	}
	if t.tmb.Alive() {
		t.tmb.Kill(nil)
	}
}

func (t *TransparentSsh) Start() error {

	// although we don't use keys for authentication, the local ssh process will
	// throw an error if it's told to look for an invalid IdentityFile, and we can
	// then re-use this private key as our "host key" when we terminate the ssh connection
	privateKey, _, err := bzssh.SetUpKeys(t.identityFile)
	if err != nil {
		return fmt.Errorf("failed to set up ssh keypair: %s", err)
	} else {
		if err := t.knownHosts.AddHostKeyPrivate(privateKey); err != nil {
			return fmt.Errorf("failed to update known_hosts file: %s", err)
		}
	}

	t.sendOutputMessage(bzssh.SshOpen, bzssh.SshOpenMessage{})

	go func() {
		defer close(t.doneChan)
		<-t.tmb.Dying()
	}()

	// the following implementation of an ssh server is based heavily on this example:
	// https://github.com/Scalingo/go-ssh-examples/blob/master/server_complex.go

	// An SSH server is represented by a ServerConfig, which holds
	// certificate details and handles authentication of ServerConns.
	config := &gossh.ServerConfig{
		NoClientAuth: true,
		PublicKeyCallback: func(c gossh.ConnMetadata, pubKey gossh.PublicKey) (*gossh.Permissions, error) {
			return &gossh.Permissions{}, nil
		},
	}
	private, _ := gossh.ParsePrivateKey(privateKey)
	config.AddHostKey(private)

	go func() {
		defer t.sshListener.Close()

		// Once a ServerConfig has been configured, tell ZLI we can accept connections
		t.zliIo.Write([]byte(readyMsg))

		// Before use, a handshake must be performed on the incoming net.Conn.
		nConn, _ := t.sshListener.Accept()
		_, chans, reqs, err := gossh.NewServerConn(nConn, config)
		if err != nil {
			t.logger.Errorf("failed to handshake: %s", err)
		}

		go gossh.DiscardRequests(reqs)

		go func() {
			for newChannel := range chans {
				// Channels have a type, depending on the application level protocol intended.
				if t := newChannel.ChannelType(); t != "session" {
					newChannel.Reject(gossh.UnknownChannelType, fmt.Sprintf("unknown channel type: %s", t))
					continue
				}

				channel, requests, err := newChannel.Accept()
				if err != nil {
					t.logger.Errorf("could not accept channel: %s", err)
					continue
				}
				t.sshChannel = channel

				// Sessions have out-of-band requests such as "shell", "pty-req" and "env"
				go func(requests <-chan *gossh.Request) {
					for req := range requests {
						ok := false
						if len(req.Payload) < sshPayloadOffset {
							t.rejectSshWithError(fmt.Sprintf("ssh payload must begin with %d bytes of metadata. Received %d bytes", sshPayloadOffset, len(req.Payload)))
							return
						}
						payloadSize := int(req.Payload[sshPayloadOffset-1])
						if len(req.Payload) < (sshPayloadOffset + payloadSize) {
							t.rejectSshWithError(fmt.Sprintf("ssh payload metadata indicated body length of %d bytes. Received %d bytes", payloadSize, len(req.Payload)-sshPayloadOffset))
							return
						}

						switch req.Type {
						// handle scp (and someday, other exec)
						case "exec":
							command := string(req.Payload[sshPayloadOffset : sshPayloadOffset+payloadSize])
							if !bzssh.IsValidScp(command) {
								t.rejectSshWithError(bzssh.UnauthorizedCommandError(fmt.Sprintf("'%s'", command)))
								return
							}

							ok = true
							go t.readFromChannel()

							sshExecMessage := bzssh.SshExecMessage{
								Command: command,
							}
							t.sendOutputMessage(bzssh.SshExec, sshExecMessage)

						// handle sftp (NOTE: looks like git works over this kind of system too)
						case "subsystem":
							command := string(req.Payload[sshPayloadOffset : sshPayloadOffset+payloadSize])

							if !bzssh.IsValidSftp(command) {
								t.rejectSshWithError(bzssh.UnauthorizedCommandError(fmt.Sprintf("'%s'", command)))
								return
							}

							ok = true
							go t.readFromChannel()

							sshExecMessage := bzssh.SshExecMessage{
								Command: command,
								Sftp:    true,
							}
							t.sendOutputMessage(bzssh.SshExec, sshExecMessage)

						// maybe someday we will allow these!
						case "shell":
							t.rejectSshWithError(bzssh.UnauthorizedCommandError("shell request"))
							return

						case "pty-req":
							t.rejectSshWithError(bzssh.UnauthorizedCommandError("PTY request"))
							return
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
			t.logger.Infof("tomb was killed. Going to stop reading from stdin")
		default:
			n, err := t.sshChannel.Read(b)
			if err != nil {
				if err == io.EOF {
					// when UPLOADING, we need to tell Agent we're done
					// if we reach this point we assume success
					t.signalSuccess()
					t.logger.Errorf("finished reading from stdin")
					t.sendOutputMessage(bzssh.SshClose, bzssh.SshCloseMessage{Reason: endedByUser})
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
	//default to stdout
	var writer io.Writer = t.sshChannel
	stream := "stdout"

	switch smsg.StreamType(smessage.Type) {
	case smsg.StdErr:
		// we treat the same as stdout but flag accordingly
		writer = t.sshChannel.Stderr()
		stream = "stderr"
		fallthrough
	case smsg.StdOut:
		if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
			t.logger.Errorf("Error decoding ssh StdOut stream content: %s", err)
		} else {
			t.logger.Infof("sending %d bytes to channel %s", len(contentBytes), stream)
			if _, err = writer.Write(contentBytes); err != nil {
				t.logger.Errorf("Error writing to channel: %s", err)
			}
			if !smessage.More {
				// when DOWNLOADING, we rely on Agent to tell us it's done
				// if we've reached this point we assume success
				t.logger.Errorf("received ssh close stream message")
				t.signalSuccess()
				t.Kill()
			}
		}
	case smsg.Error:
		// let the ZLI know if the agent encountered a policy error
		t.logger.Errorf("received an error from the agent")
		if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
			t.logger.Errorf("error decoding ssh StdOut stream content: %s", err)
		} else {
			t.zliIo.WriteErr([]byte(contentBytes))
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

func (t *TransparentSsh) rejectSshWithError(errMsg string) {
	t.logger.Errorf(errMsg)
	t.zliIo.WriteErr([]byte(errMsg))
	t.Kill()
}
