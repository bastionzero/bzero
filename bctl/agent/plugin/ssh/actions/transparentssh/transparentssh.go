package transparentssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/ssh/actions/opaquessh"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize     = 128 * 1024
	writeDeadline = 5 * time.Second
)

type AuthorizedKeysInterface interface {
	Add(pubkey string) error
}

type TransparentSsh struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	// channel for letting the plugin know we're done
	doneChan chan struct{}

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	//stdInReader *StdReader
	conn    *gossh.Client
	session *gossh.Session

	remoteConnection *net.TCPConn
	authorizedKeys   AuthorizedKeysInterface

	stdInChan chan []byte
}

func New(logger *logger.Logger, doneChan chan struct{}, ch chan smsg.StreamMessage, conn *net.TCPConn, authKeys AuthorizedKeysInterface) *TransparentSsh {

	return &TransparentSsh{
		logger:           logger,
		doneChan:         doneChan,
		streamOutputChan: ch,
		remoteConnection: conn,
		authorizedKeys:   authKeys,
		stdInChan:        make(chan []byte, 10),
	}
}

func (t *TransparentSsh) Kill() {
	t.tmb.Kill(nil)
	if t.remoteConnection != nil {
		(*t.remoteConnection).Close()
	}
	t.conn.Close()
	t.session.Close()
	t.tmb.Wait()
}

func (t *TransparentSsh) Receive(action string, actionPayload []byte) ([]byte, error) {

	// Update the logger action
	t.logger = t.logger.GetActionLogger(action)
	switch ssh.SshSubAction(action) {
	case ssh.SshOpen:
		var openRequest ssh.SshOpenMessage
		if err := json.Unmarshal(actionPayload, &openRequest); err != nil {
			return nil, fmt.Errorf("malformed default SSH action payload %s", string(actionPayload))
		}
		/*
			else if err = t.authorizedKeys.Add(string(openRequest.PublicKey)); err != nil {
				return nil, err
			}
		*/

		// do I need to send a ready message?
		return t.start(openRequest, action)
	case ssh.SshInput:

		// Deserialize the action payload, the only action passed is input
		var inputRequest ssh.SshInputMessage
		if err := json.Unmarshal(actionPayload, &inputRequest); err != nil {
			return nil, fmt.Errorf("unable to unmarshal default SSH input message: %s", err)
		}

		t.logger.Infof("the data is %s", inputRequest.Data)

		// TODO: send these to a channel that is piped to ssh
		t.stdInChan <- inputRequest.Data

		/*
			if err != nil {
				t.logger.Errorf("error writing to local TCP connection: %s", err)
				t.Kill()
			}
		*/

	case ssh.SshClose:
		// Deserialize the action payload
		var closeRequest ssh.SshCloseMessage
		if jerr := json.Unmarshal(actionPayload, &closeRequest); jerr != nil {
			// not a fatal error, we can still just close without a reason
			t.logger.Errorf("unable to unmarshal default SSH close message: %s", jerr)
		}

		t.logger.Infof("Ending TCP connection because we received this close message from daemon: %s", closeRequest.Reason)
		t.Kill()

		return actionPayload, nil
	default:
		return nil, fmt.Errorf("unhandled stream action: %s", action)
	}

	return []byte{}, nil
}

func (t *TransparentSsh) start(openRequest ssh.SshOpenMessage, action string) ([]byte, error) {

	t.logger.Errorf("open request %+v", openRequest)
	host := "localhost:22"
	privateBytes, publicBytes, _ := opaquessh.GenerateKeys()
	t.authorizedKeys.Add(string(publicBytes))
	// FIXME: add to authorizedKeys? But how does it know where to look... hopefully this user!

	var err error
	var signer gossh.Signer
	//var hostKey gossh.PublicKey

	signer, err = gossh.ParsePrivateKey(privateBytes)
	if err != nil {
		return nil, err
	}

	conf := &gossh.ClientConfig{
		// FIXME: get this here...
		User: "ec2-user",
		// FIXME: get rid of that...
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Auth: []gossh.AuthMethod{
			gossh.PublicKeys(signer),
		},
	}

	t.conn, err = gossh.Dial("tcp", host, conf)
	if err != nil {
		return nil, fmt.Errorf("dial error: %s", err)
	}

	var stdin io.WriteCloser
	var stdout, stderr io.Reader

	t.session, err = t.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("session err: %s", err)
	}

	stdin, err = t.session.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe err: %s", err)
	}

	stdout, err = t.session.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe err: %s", err)
	}

	stderr, err = t.session.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe err: %s", err)
	}

	/*
		stdOutWriter := NewStdWriter(t.streamOutputChan, t.streamMessageVersion, string(ssh.TransparentSsh), smsg.StdOut)
		stdErrWriter := NewStdWriter(t.streamOutputChan, t.streamMessageVersion, string(ssh.TransparentSsh), smsg.StdErr)
		t.stdInReader = NewStdReader(string(ssh.SshInput), t.stdInChan)
	*/

	go func() {
		for {
			d := <-t.stdInChan
			t.logger.Errorf("Writing %d bytes to stdin: %s", len(d), d)
			_, err := stdin.Write(d)
			if err != nil {
				// FIXME: probably kill here
				t.logger.Errorf("stdin write err: %s", err)
			}
		}
	}()

	// TODO: goroutines to read from stdout/stderr and write them back to daemon
	// FIXME: may not be these...
	go func() {
		b := make([]byte, chunkSize)
		t.logger.Errorf("reading stdout...")
		for {
			select {
			case <-t.tmb.Dying():
				return
			default:
				if n, err := stdout.Read(b); !t.tmb.Alive() {
					return
				} else if err != nil {
					if err == io.EOF {
						t.sendStreamMessage(0, smsg.StdOut, false, b[:n])
						return
					}
				} else if n > 0 {
					t.logger.Debugf("Read %d bytes from local SSH stdout: %s", n, b[:n])
					t.sendStreamMessage(0, smsg.StdOut, true, b[:n])
				}
			}
		}
	}()

	go func() {
		b := make([]byte, chunkSize)
		t.logger.Errorf("reading stderr...")
		for {
			select {
			case <-t.tmb.Dying():
				return
			default:
				if n, err := stderr.Read(b); !t.tmb.Alive() {
					return
				} else if err != nil && n > 0 {
					t.logger.Debugf("Read %d bytes from local SSH stderr", n)
					t.sendStreamMessage(0, smsg.StdErr, true, b[:n])
				}
			}
		}
	}()

	modes := gossh.TerminalModes{
		gossh.ECHO: 0, // disable echoing
		/*
			gossh.VLNEXT: 1,
			gossh.ISIG:   1,
			gossh.ECHOE:  1,
			gossh.ECHOK:  1,
		*/

		gossh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		gossh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}

	if err := t.session.RequestPty("xterm", 29, 149, modes); err != nil {
		t.logger.Errorf("request for pseudo terminal failed: ", err)
	}

	err = t.session.Shell()
	if err != nil {
		t.logger.Errorf("shell issue: %s", err)
	} else {
		t.logger.Errorf("Uh uh - it's turtle time")
	}

	// Update our remote connection
	return []byte{}, nil
}

func (t *TransparentSsh) sendStreamMessage(sequenceNumber int, streamType smsg.StreamType, more bool, contentBytes []byte) {
	t.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion:  t.streamMessageVersion,
		SequenceNumber: sequenceNumber,
		// FIXME: transparent
		Action:  string(ssh.TransparentSsh),
		Type:    streamType,
		More:    more,
		Content: base64.StdEncoding.EncodeToString(contentBytes),
	}
}
