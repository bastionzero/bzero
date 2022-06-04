package opaquessh

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

	"github.com/kr/pty"
	gossh "golang.org/x/crypto/ssh"
	"gopkg.in/tomb.v2"

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

type OpaqueSsh struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	outboxQueue chan plugin.ActionWrapper
	doneChan    chan struct{}

	// channel where we push from StdIn
	stdInChan chan []byte

	identityFile string

	filIo bzio.BzFileIo
	stdIo io.ReadWriter
}

func New(
	logger *logger.Logger,
	outboxQueue chan plugin.ActionWrapper,
	doneChan chan struct{},
	identityFile string,
	filIo bzio.BzFileIo,
	stdIo io.ReadWriter,
) *OpaqueSsh {

	return &OpaqueSsh{
		logger:       logger,
		outboxQueue:  outboxQueue,
		doneChan:     doneChan,
		stdInChan:    make(chan []byte, InputBufferSize),
		identityFile: identityFile,
		filIo:        filIo,
		stdIo:        stdIo,
	}
}

func (d *OpaqueSsh) Done() <-chan struct{} {
	return d.doneChan
}

func (d *OpaqueSsh) Kill() {
	d.tmb.Kill(nil)
}

func (d *OpaqueSsh) Start() error {

	var privateKey, publicKey []byte

	// if we already have a private key, use it. Otherwise, create a new one
	// NOTE: it is technically possible for this to create a one-time race if two SSH processes
	// are kicked off *and* the user just logged in. However this is unlikely and can be resolved
	// if/when we upgrade the SSH architecture
	if publicKeyRsa, err := readPublicKeyRsa(d.identityFile, d.filIo); err == nil {
		if publicKey, err = generatePublicKey(publicKeyRsa); err != nil {
			return fmt.Errorf("error decoding temporary public key: %s", err)
		} else {
			d.logger.Debugf("using existing temporary keys")
		}
	} else {
		d.logger.Debugf("generating new temporary keys")
		privateKey, publicKey, err = GenerateKeys()
		if err != nil {
			return fmt.Errorf("error generating temporary keys: %s", err)
		} else if err := d.filIo.WriteFile(d.identityFile, privateKey, 0600); err != nil {
			return fmt.Errorf("error writing temporary private key: %s", err)
		}
	}

	sshOpenMessage := ssh.SshOpenMessage{
		PublicKey:            []byte(publicKey),
		StreamMessageVersion: smsg.CurrentSchema,
	}

	d.sendOutputMessage(ssh.SshOpen, sshOpenMessage)

	go func() {
		defer close(d.doneChan)
		<-d.tmb.Dying()
	}()

	/*

		d.tmb.Go(func() error {
			b := make([]byte, InputBufferSize)

			for {
				select {
				case <-d.tmb.Dying():
					return nil
				default:
					if n, err := d.stdIo.Read(b); !d.tmb.Alive() {
						return nil
					} else if err != nil {
						if err == io.EOF {
							d.sendOutputMessage(ssh.SshClose, ssh.SshCloseMessage{Reason: "SSH session ended"})
							return fmt.Errorf("finished reading from stdin")
						}
						return fmt.Errorf("error reading from Stdin: %s", err)
					} else if n > 0 {
						d.logger.Debugf("Read %d bytes from local SSH", n)
						d.stdInChan <- b[:n]
					}
				}
			}
		})
	*/

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

	d.logger.Infof("Making config")
	newPrivate, _, _ := GenerateKeys()
	private, _ := gossh.ParsePrivateKey(newPrivate)
	config.AddHostKey(private)
	d.logger.Infof("Made config")
	go func() {
		// Once a ServerConfig has been configured, connections can be
		// accepted.
		d.logger.Infof("Gonna listen")
		listener, err := net.Listen("tcp", ":2222")
		if err != nil {
			d.logger.Errorf("failed to listen for connection: ", err)
		}

		defer listener.Close()

		d.logger.Infof("Accepting...")
		for {
			nConn, _ := listener.Accept()
			d.logger.Infof("accepted")
			// Before use, a handshake must be performed on the incoming
			// net.Conn.
			d.logger.Infof("Trying to make thingies??")
			_, chans, reqs, err := gossh.NewServerConn(nConn, config)

			d.logger.Infof("Made thingies??")
			if err != nil {
				d.logger.Errorf("failed to handshake: ", err)
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
					if err != nil {
						log.Printf("could not accept channel (%s)", err)
						continue
					}

					// allocate a terminal for this channel
					log.Print("creating pty...")
					// Create new pty
					f, tty, err := pty.Open()
					if err != nil {
						log.Printf("could not start pty (%s)", err)
						continue
					}

					var shell string
					shell = os.Getenv("SHELL")
					if shell == "" {
						shell = DEFAULT_SHELL
					}

					// Sessions have out-of-band requests such as "shell", "pty-req" and "env"
					go func(in <-chan *gossh.Request) {
						for req := range in {
							log.Printf("%v %s", req.Payload, req.Payload)
							ok := false
							switch req.Type {
							case "exec":
								ok = true
								command := string(req.Payload[4 : req.Payload[3]+4])
								d.logger.Infof("Lucie, the command is %s", command)
								cmd := exec.Command(shell, []string{"-c", command}...)

								cmd.Stdout = channel
								cmd.Stderr = channel
								cmd.Stdin = channel

								err := cmd.Start()
								if err != nil {
									log.Printf("could not start command (%s)", err)
									continue
								}

								// teardown session
								go func() {
									_, err := cmd.Process.Wait()
									if err != nil {
										log.Printf("failed to exit bash (%s)", err)
									}
									channel.Close()
									log.Printf("session closed")
								}()
							case "shell":
								cmd := exec.Command(shell)
								cmd.Env = []string{"TERM=xterm"}
								err := PtyRun(cmd, tty)
								if err != nil {
									log.Printf("%s", err)
								}

								// Teardown session
								var once sync.Once
								close := func() {
									channel.Close()
									log.Printf("session closed")
								}

								// Pipe session to bash and visa-versa
								go func() {
									io.Copy(channel, f)
									once.Do(close)
								}()

								go func() {
									io.Copy(f, channel)
									once.Do(close)
								}()

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
								SetWinsize(f.Fd(), w, h)
								log.Printf("pty-req '%s'", termEnv)
							case "window-change":
								w, h := parseDims(req.Payload)
								SetWinsize(f.Fd(), w, h)
								continue //no response
							}

							if !ok {
								log.Printf("declining %s request...", req.Type)
							}

							req.Reply(ok, nil)
						}
					}(requests)
				}
			}()
		}

		//keyID := sConn.Permissions.Extensions["key-id"]

		// Service the incoming Channel channel.
	}()

	return nil
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
	log.Printf("window resize %dx%d", w, h)
	ws := &Winsize{Width: uint16(w), Height: uint16(h)}
	syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(ws)))
}

func (d *OpaqueSsh) ReceiveStream(smessage smsg.StreamMessage) {
	d.logger.Debugf("Default ssh received %+v stream", smessage.Type)
	switch smsg.StreamType(smessage.Type) {
	case smsg.StdOut:
		if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
			d.logger.Errorf("Error decoding ssh StdOut stream content: %s", err)
		} else {
			if _, err = d.stdIo.Write(contentBytes); err != nil {
				d.logger.Errorf("Error writing to Stdout: %s", err)
			}
			if !smessage.More {
				d.tmb.Kill(fmt.Errorf("received ssh close stream message"))
				return
			}
		}
	case smsg.Error:
		d.tmb.Kill(fmt.Errorf("received an error from the agent"))
		return
	default:
		d.logger.Errorf("unhandled stream type: %s", smessage.Type)
	}
}

func (d *OpaqueSsh) sendSshInputMessage(bs []byte) {
	// Send all accumulated input in an sshInput data message
	sshInputDataMessage := ssh.SshInputMessage{
		Data: bs,
	}
	d.sendOutputMessage(ssh.SshInput, sshInputDataMessage)
}

func (d *OpaqueSsh) sendOutputMessage(action ssh.SshSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	d.outboxQueue <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}
}
