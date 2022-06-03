package opaquessh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"github.com/creack/pty"
	glssh "github.com/gliderlabs/ssh"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/bzio"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
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

func setWinsize(f *os.File, w, h int) {
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCSWINSZ),
		uintptr(unsafe.Pointer(&struct{ h, w, x, y uint16 }{uint16(h), uint16(w), 0, 0})))
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

	// Once a ServerConfig has been configured, connections can be
	// accepted.
	d.logger.Infof("Gonna listen %s", publicKey)
	//listener, err := net.Listen("tcp", ":2222")
	//if err != nil {
	//	d.logger.Errorf("failed to listen for connection: ", err)
	//}

	//defer listener.Close()

	d.logger.Infof("Accepting...")
	//nConn, _ := listener.Accept()
	d.logger.Infof("accepted")

	glssh.Handle(func(s glssh.Session) {
		d.logger.Infof("command %s", strings.Join(s.Command(), ""))
		d.logger.Infof("raw command %s", s.RawCommand())
		d.logger.Infof("subsystem %s", s.Subsystem())
		d.logger.Infof("user %s", s.User())
		cmd := exec.Command("sh")
		ptyReq, winCh, isPty := s.Pty()
		if isPty {
			cmd.Env = append(cmd.Env, fmt.Sprintf("TERM=%s", ptyReq.Term))
			f, err := pty.Start(cmd)
			if err != nil {
				panic(err)
			}
			go func() {
				for win := range winCh {
					setWinsize(f, win.Width, win.Height)
				}
			}()
			go func() {
				io.Copy(f, s) // stdin
			}()
			io.Copy(s, f) // stdout
			cmd.Wait()
		} else {
			io.WriteString(s, "No PTY requested.\n")
			s.Exit(1)
		}
	})

	d.logger.Infof("starting ssh server on port 2222...")
	glssh.ListenAndServe(":2222", nil)
	/*
		sshOpenMessage := ssh.SshOpenMessage{
			PublicKey:            []byte(publicKey),
			StreamMessageVersion: smsg.CurrentSchema,
		}

		d.sendOutputMessage(ssh.SshOpen, sshOpenMessage)

		go func() {
			defer close(d.doneChan)
			<-d.tmb.Dying()
		}()
	*/

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

	/*
		// An SSH server is represented by a ServerConfig, which holds
		// certificate details and handles authentication of ServerConns.
		config := &gossh.ServerConfig{
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

		// Before use, a handshake must be performed on the incoming
		// net.Conn.
		d.logger.Infof("Trying to make thingies??")
		_, chans, reqs, err := gossh.NewServerConn(nConn, config)

		d.logger.Infof("Made thingies??")
		if err != nil {
			d.logger.Errorf("failed to handshake: ", err)
		}

		go gossh.DiscardRequests(reqs)

		//keyID := sConn.Permissions.Extensions["key-id"]

	*/
	/*
		// Service the incoming Channel channel.
		for newChan := range chans {
			d.logger.Errorf("I'm a cool loop")
			if newChan.ChannelType() != "session" {
				_ = newChan.Reject(gossh.UnknownChannelType, "unknown channel type")
				continue
			}

			ch, requests, err := newChan.Accept()
			if err != nil {
				d.logger.Errorf("Error accepting channel: %v", err)
				continue
			}

			// Sessions have out-of-band requests such as "shell",
			// "pty-req" and "env".  Here we handle only the
			// "shell" request.
			go func(in <-chan *gossh.Request) {
				for req := range in {
					req.Reply(req.Type == "shell", nil)
				}
			}(requests)

			term := terminal.NewTerminal(ch, "> ")
			d.logger.Errorf("Term stuff")

			go func() {
				d.logger.Errorf("So...")
				defer ch.Close()
				for {
					d.logger.Errorf("Terminals...")
					line, err := term.ReadLine()
					if err != nil {
						d.logger.Errorf("What's wrong is: %s", err)
						break
					}
					fmt.Println(line)

					d.logger.Errorf("They're cool...")
				}
			}()
			d.logger.Errorf("Why am I here though")

			/*

				go func(in <-chan *gossh.Request) {
					defer func() {
						_ = ch.Close()
					}()
					for req := range in {
						fmt.Printf("I got %s", req.Payload)
						d.logger.Errorf("I got %s", req.Payload)
					}
				}(reqs)
	*/
	//}

	return nil
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
