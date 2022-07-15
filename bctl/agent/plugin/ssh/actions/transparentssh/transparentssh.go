package transparentssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	gossh "golang.org/x/crypto/ssh"
	"gopkg.in/tomb.v2"

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

	conn    *gossh.Client
	session *gossh.Session

	stdInChan chan []byte
}

func New(logger *logger.Logger, doneChan chan struct{}, ch chan smsg.StreamMessage, conn *gossh.Client) *TransparentSsh {
	return &TransparentSsh{
		logger:           logger,
		doneChan:         doneChan,
		streamOutputChan: ch,
		stdInChan:        make(chan []byte, 10),
		conn:             conn,
	}
}

func (t *TransparentSsh) Kill() {
	if t.session != nil {
		t.session.Close()
	}
	if t.conn != nil {
		t.conn.Close()
	}
	if t.tmb.Alive() {
		t.tmb.Kill(nil)
		t.tmb.Wait()
	}
}

func (t *TransparentSsh) Receive(action string, actionPayload []byte) ([]byte, error) {

	switch bzssh.SshSubAction(action) {
	case bzssh.SshOpen:
		var openRequest bzssh.SshOpenMessage
		if err := json.Unmarshal(actionPayload, &openRequest); err != nil {
			return nil, fmt.Errorf("unable to unmarshal transparent ssh open message: %s", err)
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

		if execRequest.Sftp {
			if !bzssh.IsValidSftp(execRequest.Command) {
				errMsg := bzssh.UnauthorizedCommandError(fmt.Sprintf("'%s'", execRequest.Command))
				t.sendStreamMessage(smsg.Error, false, []byte(errMsg))
				return nil, fmt.Errorf(errMsg)
			} else {
				// if using sftp, we have nothing to exec; just tell the server what protocol to use
				// session will be initialized since Open will always come before Exec
				t.session.RequestSubsystem(execRequest.Command)
			}
		} else if !bzssh.IsValidScp(execRequest.Command) {
			errMsg := bzssh.UnauthorizedCommandError(fmt.Sprintf("'%s'", execRequest.Command))
			t.sendStreamMessage(smsg.Error, false, []byte(errMsg))
			return nil, fmt.Errorf(errMsg)
		} else {
			// because scp takes further inputs after execution begins, we can't wait on this to bring a synchronous error
			t.exec(execRequest.Command)
		}

	case bzssh.SshClose:
		// Deserialize the action payload
		var closeRequest bzssh.SshCloseMessage
		if jerr := json.Unmarshal(actionPayload, &closeRequest); jerr != nil {
			// not a fatal error, we can still just close without a reason
			t.logger.Errorf("unable to unmarshal transparent ssh close message: %s", jerr)
		}

		t.logger.Infof("Ending SSH session because we received this close message from daemon: %s", closeRequest.Reason)
		t.sendStreamMessage(smsg.Stop, false, []byte{})
		t.Kill()
		return actionPayload, nil

	default:
		return nil, fmt.Errorf("unhandled stream action: %s", action)
	}

	return []byte{}, nil
}

func (t *TransparentSsh) start(openRequest bzssh.SshOpenMessage, action string) ([]byte, error) {

	// the following implementation of an ssh client is heavily based on this example:
	// https://medium.com/@marcus.murray/go-ssh-client-shell-session-c4d40daa46cd

	var err error

	t.session, err = t.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("session err: %s", err)
	}

	stdin, err := t.session.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe err: %s", err)
	}

	stdout, err := t.session.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe err: %s", err)
	}

	stderr, err := t.session.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe err: %s", err)
	}

	// track goroutines for how we communicate with stdin, stdout, stderr
	t.tmb.Go(func() error {
		defer close(t.doneChan)
		t.tmb.Go(func() error {
			return t.readPipe(stdout, smsg.StdOut, "stdout")
		})
		t.tmb.Go(func() error {
			return t.readPipe(stderr, smsg.StdErr, "stderr")
		})

		for {
			select {
			case <-t.tmb.Dying():
				t.logger.Infof("tomb was killed. Going to stop writing to stdin")
				return nil
			case d := <-t.stdInChan:
				t.logger.Debugf("Writing %d bytes to stdin", len(d))
				_, err := stdin.Write(d)
				if err != nil {
					if err == io.EOF {
						t.logger.Infof("Finished writing to stdin")
						return nil
					}
					t.logger.Errorf("error writing to stdin: %s", err)
					return err
				}
			}
		}
	})

	// Update our remote connection
	return []byte{}, nil
}

func (t *TransparentSsh) exec(command string) {
	t.session.Start(command)
	go func() {
		err := t.session.Wait()
		if err != nil {
			// Start returns this error if the server does not return an exit code, which appears to be the case for scp
			if _, ok := err.(*gossh.ExitMissingError); !ok {
				t.logger.Errorf("command exited with nonzero exit status: %s", err)
			}
		} else {
			t.logger.Debugf("finished execution")
		}
	}()
}

func (t *TransparentSsh) readPipe(pipe io.Reader, messageType smsg.StreamType, pipeName string) error {
	b := make([]byte, chunkSize)
	for {
		select {
		case <-t.tmb.Dying():
			return nil
		default:
			if n, err := pipe.Read(b); !t.tmb.Alive() {
				return nil
			} else if err != nil {
				if err == io.EOF {
					t.logger.Infof("Finished reading from %s", pipeName)
					t.sendStreamMessage(messageType, false, b[:n])
					return nil
				}
				t.logger.Errorf("error reading from %s: %s", pipeName, err)
				return err
			} else if n > 0 {
				t.logger.Debugf("Read %d bytes from local SSH %s", n, pipeName)
				t.sendStreamMessage(messageType, true, b[:n])
			}
		}
	}
}

func (t *TransparentSsh) sendStreamMessage(streamType smsg.StreamType, more bool, contentBytes []byte) {
	t.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion: t.streamMessageVersion,
		Action:        string(bzssh.TransparentSsh),
		Type:          streamType,
		More:          more,
		Content:       base64.StdEncoding.EncodeToString(contentBytes),
	}
}
