package defaultssh

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	"bastionzero.com/bctl/v1/bzerolib/services/fileservice"
	"bastionzero.com/bctl/v1/bzerolib/services/ioservice"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	InputBufferSize   = 8 * 1024
	InputDebounceTime = 5 * time.Millisecond
	writeDeadline     = 5 * time.Second
)

type DefaultSsh struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	outboxQueue chan plugin.ActionWrapper
	doneChan    chan struct{}

	// channel where we push from StdIn
	stdInChan chan []byte

	identityFile string

	fileService fileservice.FileService
	ioService   ioservice.IoService

	remoteAddress *net.TCPAddr
}

func New(
	logger *logger.Logger,
	outboxQueue chan plugin.ActionWrapper,
	doneChan chan struct{},
	identityFile string,
	fileService fileservice.FileService,
	ioService ioservice.IoService,
) *DefaultSsh {

	if raddr, err := net.ResolveTCPAddr("tcp", "localhost:2022"); err != nil {
		logger.Errorf("Failed to resolve remote address: %s", err)
		return nil
	} else {
		return &DefaultSsh{
			logger:        logger,
			outboxQueue:   outboxQueue,
			doneChan:      doneChan,
			stdInChan:     make(chan []byte, InputBufferSize),
			identityFile:  identityFile,
			fileService:   fileService,
			ioService:     ioService,
			remoteAddress: raddr,
		}
	}
}

func (d *DefaultSsh) Done() <-chan struct{} {
	return d.doneChan
}

func (d *DefaultSsh) Kill() {
	d.tmb.Kill(nil)
}

func (d *DefaultSsh) Start() error {

	var privateKey, publicKey []byte

	// if we already have a private key, use it. Otherwise, create a new one
	if publicKeyRsa, err := readPublicKeyRsa(d.identityFile, d.fileService); err == nil {
		if publicKey, err = generatePublicKey(publicKeyRsa); err != nil {
			return fmt.Errorf("error decoding temporary public key: %s", err)
		} else {
			d.logger.Debugf("using existing temporary keys!?!?")
		}
	} else {
		d.logger.Debugf("generating new temporary keys")
		privateKey, publicKey, err = GenerateKeys()
		if err != nil {
			return fmt.Errorf("error generating temporary keys: %s", err)
		} else if err := d.fileService.WriteFile(d.identityFile, privateKey, 0600); err != nil {
			return fmt.Errorf("error writing temporary private key: %s", err)
		}
	}

	// Once a ServerConfig has been configured, connections can be
	// accepted.
	d.logger.Infof("Gonna listen")
	listener, err := net.Listen("tcp", ":2222")
	if err != nil {
		d.logger.Errorf("failed to listen for connection: ", err)
	}

	defer listener.Close()

	d.logger.Infof("Accepting...")
	nConn, _ := listener.Accept()
	d.logger.Infof("accepted")

	sshOpenMessage := ssh.SshOpenMessage{
		PublicKey:            []byte(publicKey),
		StreamMessageVersion: smsg.CurrentSchema,
	}

	d.sendOutputMessage(ssh.SshOpen, sshOpenMessage)

	go d.sendStdIn()
	go func() {
		defer close(d.doneChan)
		<-d.tmb.Dying()
	}()

	d.tmb.Go(func() error {
		b := make([]byte, InputBufferSize)

		for {
			select {
			case <-d.tmb.Dying():
				return nil
			default:
				if n, err := d.ioService.Read(b); !d.tmb.Alive() {
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

	// Service the incoming Channel channel.
	for newChan := range chans {
		d.logger.Errorf("I'm a cool loop")
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(gossh.UnknownChannelType, "unknown channel type")
			continue
		}

		ch, reqs, err := newChan.Accept()
		if err != nil {
			d.logger.Errorf("Error accepting channel: %v", err)
			continue
		}

		go func(in <-chan *gossh.Request) {
			defer func() {
				_ = ch.Close()
			}()
			for req := range in {
				fmt.Printf("I got %s", req.Payload)
				d.logger.Errorf("I got %s", req.Payload)
			}
		}(reqs)
	}

	return nil

}

func (d *DefaultSsh) ReceiveStream(smessage smsg.StreamMessage) {

	/*
		d.logger.Debugf("Default ssh received %+v stream", smessage.Type)
		switch smsg.StreamType(smessage.Type) {
		case smsg.StdOut:
			if contentBytes, err := base64.StdEncoding.DecodeString(smessage.Content); err != nil {
				d.logger.Errorf("Error decoding ssh StdOut stream content: %s", err)
			} else {
				if _, err = d.ioService.Write(contentBytes); err != nil {
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
	*/
}

// processes input channel by debouncing all keypresses within a time interval
func (d *DefaultSsh) sendStdIn() {
	inputBuf := make([]byte, InputBufferSize)
	inputBuf = inputBuf[:0]

	for {
		select {
		case <-d.tmb.Dying():
			return
		case b := <-d.stdInChan:
			inputBuf = append(inputBuf, b...)
		case <-time.After(InputDebounceTime):
			if len(inputBuf) >= 1 {

				/*
					// Set a deadline for the write so we don't block forever
					(*d.remoteConnection).SetWriteDeadline(time.Now().Add(writeDeadline))
					if _, err := (*d.remoteConnection).Write(inputBuf); !d.tmb.Alive() {
						return
					} else if err != nil {
						d.logger.Errorf("error writing to local TCP connection: %s", err)
						d.Kill()
					}
				*/
				/*
					// Send all accumulated input in an sshInput data message
					sshInputDataMessage := ssh.SshInputMessage{
						Data: inputBuf,
					}
					d.sendOutputMessage(ssh.SshInput, sshInputDataMessage)

				*/

				// clear the input buffer by slicing it to size 0 which will still
				// keep memory allocated for the underlying capacity of the slice
				inputBuf = inputBuf[:0]
			}
		}
	}
}

func (d *DefaultSsh) sendOutputMessage(action ssh.SshSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	d.outboxQueue <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: payloadBytes,
	}
}
