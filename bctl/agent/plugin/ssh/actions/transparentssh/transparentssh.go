package transparentssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	gossh "golang.org/x/crypto/ssh"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/ssh/authorizedkeys"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize = 64 * 1024
)

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

	authorizedKeys authorizedkeys.IAuthorizedKeys

	stdInChan     chan []byte
	targetUser    string
	remoteADdress string
}

func New(logger *logger.Logger, doneChan chan struct{}, ch chan smsg.StreamMessage, authKeys authorizedkeys.IAuthorizedKeys, targetUser string, remoteAddress string) *TransparentSsh {

	return &TransparentSsh{
		logger:           logger,
		doneChan:         doneChan,
		streamOutputChan: ch,
		authorizedKeys:   authKeys,
		targetUser:       targetUser,
		stdInChan:        make(chan []byte, 10),
		remoteADdress:    remoteAddress,
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
	switch bzssh.SshSubAction(action) {
	case bzssh.SshOpen:
		var openRequest bzssh.SshOpenMessage
		if err := json.Unmarshal(actionPayload, &openRequest); err != nil {
			return nil, fmt.Errorf("malformed transparent ssh action payload %s", string(actionPayload))
		}
		return t.start(openRequest, action)

	case bzssh.SshInput:
		// Deserialize the action payload, the only action passed is input
		var inputRequest bzssh.SshInputMessage
		if err := json.Unmarshal(actionPayload, &inputRequest); err != nil {
			return nil, fmt.Errorf("unable to unmarshal transparent ssh input message: %s", err)
		}
		t.stdInChan <- inputRequest.Data

	case bzssh.SshExec:
		// Deserialize the action payload, the only action passed is input
		var execRequest bzssh.SshExecMessage
		if err := json.Unmarshal(actionPayload, &execRequest); err != nil {
			return nil, fmt.Errorf("unable to unmarshal transparent SSH exec message: %s", err)
		}

		if !bzssh.IsValidScp(execRequest.Command) {
			// TODO: tell daemon to tell ZLI about this...
			errMsg := bzssh.UnauthorizedCommandError(execRequest.Command)
			t.sendStreamMessage(0, smsg.Error, false, []byte(errMsg))
			return nil, fmt.Errorf(errMsg)
		}

		// because scp takes further inputs after execution begins, we can't wait on this to bring a syncrhonous error
		t.exec(execRequest.Command)

	case bzssh.SshClose:
		// Deserialize the action payload
		var closeRequest bzssh.SshCloseMessage
		if jerr := json.Unmarshal(actionPayload, &closeRequest); jerr != nil {
			// not a fatal error, we can still just close without a reason
			t.logger.Errorf("unable to unmarshal transparent ssh close message: %s", jerr)
		}

		t.logger.Infof("Ending SSH session because we received this close message from daemon: %s", closeRequest.Reason)
		t.Kill()
		return actionPayload, nil

	default:
		return nil, fmt.Errorf("unhandled stream action: %s", action)
	}

	return []byte{}, nil
}

func (t *TransparentSsh) start(openRequest bzssh.SshOpenMessage, action string) ([]byte, error) {

	privateBytes, publicBytes, _ := bzssh.GenerateKeys()
	t.authorizedKeys.Add(string(publicBytes))

	// the following implementation of an ssh client is heavily based on thsi example:
	// https://medium.com/@marcus.murray/go-ssh-client-shell-session-c4d40daa46cd

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

	t.conn, err = gossh.Dial("tcp", t.remoteADdress, conf)
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

	go func() {
		for {
			d := <-t.stdInChan
			t.logger.Debugf("Writing %d bytes to stdin", len(d))
			_, err := stdin.Write(d)
			if err != nil {
				if err == io.EOF {
					t.logger.Infof("Finished writing to stdin")
					return
				}
				t.logger.Errorf("error writing to stdin: %s", err)
				t.Kill()
				return
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
				if n, err := stdout.Read(b); !t.tmb.Alive() {
					return
				} else if err != nil {
					if err == io.EOF {
						t.logger.Infof("Finished reading from stdout")
						t.sendStreamMessage(0, smsg.StdOut, false, b[:n])
						return
					}
					t.logger.Errorf("error reading from stdout: %s", err)
					t.Kill()
					return
				} else if n > 0 {
					t.logger.Debugf("Read %d bytes from local SSH stdout", n)
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
				} else if err != nil && err != io.EOF {
					t.logger.Errorf("error reading from stderr: %s", err)
					t.Kill()
					return
				}
			}
		}
	}()
	// Update our remote connection
	return []byte{}, nil
}

func (t *TransparentSsh) exec(command string) {
	t.session.Start(command)
	go func() {
		err := t.session.Wait()
		// TODO: scp seems to end silently without a status, which is fine for us but bothers Wait
		// this seems like too brittle a way to check for this message but I'm not sure how else we would
		if err != nil && err.Error() != "wait: remote command exited without exit status or exit signal" {
			t.logger.Errorf("command exited with nonzero exit status: %s", err)

		} else {
			t.logger.Debugf("finished execution")
		}
		t.Kill()
	}()
}

func (t *TransparentSsh) sendStreamMessage(sequenceNumber int, streamType smsg.StreamType, more bool, contentBytes []byte) {
	t.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion:  t.streamMessageVersion,
		SequenceNumber: sequenceNumber,
		Action:         string(bzssh.TransparentSsh),
		Type:           streamType,
		More:           more,
		Content:        base64.StdEncoding.EncodeToString(contentBytes),
	}
}
