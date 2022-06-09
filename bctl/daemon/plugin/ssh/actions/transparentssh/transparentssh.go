package transparentssh

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

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

	sshChannel    io.ReadWriteCloser
	agentDoneChan chan struct{}
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
		logger:        logger,
		outboxQueue:   outboxQueue,
		doneChan:      doneChan,
		stdInChan:     make(chan []byte),
		identityFile:  identityFile,
		filIo:         filIo,
		stdIo:         stdIo,
		agentDoneChan: make(chan struct{}),
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
		// Once a ServerConfig has been configured, connections can be
		// accepted.
		t.logger.Infof("Gonna listen")
		listener, err := net.Listen("tcp", ":2222")
		if err != nil {
			t.logger.Errorf("failed to listen for connection: ", err)
		}
		t.stdIo.Write([]byte(readyMsg))

		defer listener.Close()

		t.logger.Infof("Accepting...")
		nConn, _ := listener.Accept()
		t.logger.Infof("accepted")
		// Before use, a handshake must be performed on the incoming
		// net.Conn.
		t.logger.Infof("Trying to make thingies??")
		_, chans, reqs, err := gossh.NewServerConn(nConn, config)

		t.logger.Infof("Made thingies??")
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
							// FIXME: read the command; if invalid, tell the ZLI that
							t.logger.Infof("Lucie, the command is %s", command)
							if !ssh.IsValidScp(command) {
								t.logger.Errorf("invalid command: this user is only allowed to perform file upload / download via scp, but recieved %s", command)
								channel.Close()
								t.Kill()
								return
							}

							ok = true

							go t.readFromChannel()
							// channel.Close()\
							// respond to the initial scp request
							//channel.Write([]byte([]uint8{0}))
							//channel.Write([]byte([]uint8{0}))

							sshExecMessage := ssh.SshExecMessage{
								Command: command,
							}
							t.sendOutputMessage(ssh.SshExec, sshExecMessage)

						case "shell":
							/* TODO: this code works okay if the agent provides a PTY, but only sends input line by line, not char by char!
							t.tmb.Go(func() error {
								b := make([]byte, InputBufferSize)
								t.logger.Errorf("Trying to read from stdin...")

								for {
									select {
									case <-t.tmb.Dying():
										channel.Close()
										return nil
									default:
										if n, err := channel.Read(b); !t.tmb.Alive() {
											return nil
										} else if err != nil {
											if err == io.EOF {
												t.sendOutputMessage(ssh.SshClose, ssh.SshCloseMessage{Reason: endedByUser})
												channel.Close()
												return fmt.Errorf("finished reading from stdin")
											}
											channel.Close()
											return fmt.Errorf("error reading from Stdin: %s", err)
										} else if n > 0 {
											t.logger.Debugf("Read %d bytes from local SSH", n)
											t.sendSshInputMessage(b[:n])
										}
									}
								}
							})
							*/

							stdWriter := NewStdWriter(t.logger, t.stdInChan)
							stdReader := NewStdReader(t.logger, t.outboxQueue, string(ssh.SshInput))

							var once sync.Once
							close := func() {
								channel.Close()
								log.Printf("session closed")
							}
							go func() {
								t.logger.Errorf("Has this ever happened to read?")
								io.Copy(channel, stdReader)
								once.Do(close)
							}()

							go func() {
								t.logger.Errorf("Has this ever happened to write?")
								io.Copy(stdWriter, channel)
								once.Do(close)
							}()

							// Teardown session
							// FIXME: figure out the right way to close channel
							/*
								var once sync.Once
								close := func() {
									channel.Close()
									t.logger.Infof("session closed")
								}
							*/

							// We don't accept any commands (Payload),
							// only the default shell.
							if len(req.Payload) == 0 {
								ok = true
							}

						case "pty-req":
							// Responding 'ok' here will let the client
							// know we have a pty ready for input
							ok = true
							// Parse body...

							termLen := req.Payload[3]
							termEnv := string(req.Payload[4 : termLen+4])
							w, h := parseDims(req.Payload[termLen+4:])
							t.logger.Infof("pty-req stuff: termlen %d termEnv %s w %d h %d", termLen, termEnv, w, h)
							//SetWinsize(f.Fd(), w, h)
							//t.logger.Infof("pty-req '%s'", termEnv)
						case "window-change":
							t.logger.Infof("Window changing??")
							/*
								w, h := parseDims(req.Payload)
								SetWinsize(f.Fd(), w, h)
								continue //no response
							*/
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

// Start assigns a pseudo-terminal tty os.File to c.Stdin, c.Stdout,
// and c.Stderr, calls c.Start, and returns the File of the tty's
// corresponding pty.
func PtyRun(c *exec.Cmd, tty *os.File) (err error) {
	defer tty.Close()
	c.Stdout = tty
	c.Stdin = tty
	c.Stderr = tty
	c.SysProcAttr = &syscall.SysProcAttr{
		Setctty: true,
		Setsid:  true,
	}
	return c.Start()
}

// parseDims extracts two uint32s from the provided buffer.

func parseDims(b []byte) (uint32, uint32) {
	w := binary.BigEndian.Uint32(b)
	h := binary.BigEndian.Uint32(b[4:])
	return w, h
}

// Winsize stores the Height and Width of a terminal.
type Winsize struct {
	Height uint16
	Width  uint16
	x      uint16 // unused
	y      uint16 // unused
}

// SetWinsize sets the size of the given pty.
func SetWinsize(fd uintptr, w, h uint32) {
	ws := &Winsize{Width: uint16(w), Height: uint16(h)}
	syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(ws)))
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
