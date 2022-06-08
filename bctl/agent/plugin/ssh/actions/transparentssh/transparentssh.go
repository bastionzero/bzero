package transparentssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/ssh/actions/opaquessh"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize     = 64 * 1024
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

	authorizedKeys AuthorizedKeysInterface

	targetUser string
}

func New(logger *logger.Logger, doneChan chan struct{}, ch chan smsg.StreamMessage, authKeys AuthorizedKeysInterface, targetUser string) *TransparentSsh {

	return &TransparentSsh{
		logger:           logger,
		doneChan:         doneChan,
		streamOutputChan: ch,
		authorizedKeys:   authKeys,
		targetUser:       targetUser,
	}
}

func (t *TransparentSsh) Kill() {
	t.tmb.Kill(nil)
	if t.session != nil {
		t.session.Close()
	}
	if t.conn != nil {
		t.conn.Close()
	}
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

		// do I need to send a ready message?
		return t.start(openRequest, action)
	case ssh.SshInput:
		return nil, fmt.Errorf("shell sessions are not supported yet vai transparent ssh")

		/* TODO: needed for shell but not supported yet
		// Deserialize the action payload, the only action passed is input
		var inputRequest ssh.SshInputMessage
		if err := json.Unmarshal(actionPayload, &inputRequest); err != nil {
			return nil, fmt.Errorf("unable to unmarshal default SSH input message: %s", err)
		}

		t.logger.Infof("the data is %s", inputRequest.Data)

		t.stdInChan <- inputRequest.Data
		*/

	case ssh.SshExec:
		// Deserialize the action payload, the only action passed is input
		var execRequest ssh.SshExecMessage
		if err := json.Unmarshal(actionPayload, &execRequest); err != nil {
			return nil, fmt.Errorf("unable to unmarshal transparent SSH exec message: %s", err)
		}

		if err := t.exec(execRequest.Command); err != nil {
			return nil, fmt.Errorf("failed to execute command %s: %s", execRequest.Command, err)
		}

	case ssh.SshClose:
		// FIXME: implement this better? I guess kill is all that's really needed...
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

	// FIXME: make this the host I got
	host := "localhost:22"
	privateBytes, publicBytes, _ := opaquessh.GenerateKeys()
	t.authorizedKeys.Add(string(publicBytes))

	var err error
	var signer gossh.Signer

	signer, err = gossh.ParsePrivateKey(privateBytes)
	if err != nil {
		return nil, err
	}

	conf := &gossh.ClientConfig{
		User: t.targetUser,
		// FIXME: figure out how to actually do this...
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Auth: []gossh.AuthMethod{
			gossh.PublicKeys(signer),
		},
	}

	t.conn, err = gossh.Dial("tcp", host, conf)
	if err != nil {
		return nil, fmt.Errorf("dial error: %s", err)
	}

	var stdout, stderr io.Reader

	t.session, err = t.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("session err: %s", err)
	}

	stdout, err = t.session.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe err: %s", err)
	}

	stderr, err = t.session.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe err: %s", err)
	}

	go func() {
		b := make([]byte, chunkSize)
		for {
			select {
			case <-t.tmb.Dying():
				return
			default:
				if n, err := stdout.Read(b); !t.tmb.Alive() {
					return
				} else if err != nil {
					if err == io.EOF {
						t.logger.Debugf("Finished reading: %v", b[:n])
						t.sendStreamMessage(0, smsg.StdOut, false, b[:n])
						return
					}
				} else if n > 0 {
					t.logger.Debugf("Read %d bytes from local SSH stdout: %v", n, b[:n])
					t.sendStreamMessage(0, smsg.StdOut, true, b[:n])
				}
			}
		}
	}()

	go func() {
		b := make([]byte, chunkSize)
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
	// Update our remote connection
	return []byte{}, nil
}

func (t *TransparentSsh) exec(command string) error {
	t.session.Run(command)
	go func() {
		err := t.session.Wait()
		if err != nil {
			t.logger.Errorf("failed to exit bash (%s)", err)
		}
		t.session.Close()
	}()
	// FIXME: reviist how this gets returned
	t.sendStreamMessage(0, smsg.StdOut, false, []byte{})
	return nil
}

func (t *TransparentSsh) sendStreamMessage(sequenceNumber int, streamType smsg.StreamType, more bool, contentBytes []byte) {
	t.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion:  t.streamMessageVersion,
		SequenceNumber: sequenceNumber,
		Action:         string(ssh.TransparentSsh),
		Type:           streamType,
		More:           more,
		Content:        base64.StdEncoding.EncodeToString(contentBytes),
	}
}
