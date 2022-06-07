package transparentssh

import (
	"bufio"
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

		return t.start(openRequest, action)
	case ssh.SshInput:

		// Deserialize the action payload, the only action passed is input
		var inputRequest ssh.SshInputMessage
		if err := json.Unmarshal(actionPayload, &inputRequest); err != nil {
			return nil, fmt.Errorf("unable to unmarshal default SSH input message: %s", err)
		}

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
		User: openRequest.TargetUser,
		// FIXME: get rid of that...
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Auth: []gossh.AuthMethod{
			gossh.PublicKeys(signer),
		},
	}

	var conn *gossh.Client

	conn, err = gossh.Dial("tcp", host, conf)
	if err != nil {
		return nil, fmt.Errorf("dial error: %s", err)
	}
	defer conn.Close()

	var session *gossh.Session
	var stdin io.WriteCloser
	var stdout, stderr io.Reader

	session, err = conn.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	stdin, err = session.StdinPipe()
	if err != nil {
		return nil, err
	}

	stdout, err = session.StdoutPipe()
	if err != nil {
		return nil, err
	}

	stderr, err = session.StderrPipe()
	if err != nil {
		return nil, err
	}

	go func() {
		for {
			d := <-t.stdInChan
			_, err := stdin.Write(d)
			if err != nil {
				// FIXME: probably kill here
				t.logger.Errorf(err.Error())
			}
		}
	}()

	// TODO: goroutines to read from stdout/stderr and write them back to daemon
	// FIXME: may not be these...
	go func() {
		scanner := bufio.NewScanner(stdout)
		for {
			if tkn := scanner.Scan(); tkn {
				rcv := scanner.Bytes()

				raw := make([]byte, len(rcv))
				copy(raw, rcv)

				// FIXME: sequence number...
				t.sendStreamMessage(0, smsg.StdOut, true, raw)
			} else {
				if scanner.Err() != nil {
					fmt.Println(scanner.Err())
				} else {
					t.sendStreamMessage(0, smsg.StdOut, false, []byte{})
				}
				return
			}
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)

		for scanner.Scan() {
			rcv := scanner.Bytes()
			raw := make([]byte, len(rcv))
			copy(raw, rcv)
			t.sendStreamMessage(0, smsg.StdErr, true, scanner.Bytes())
		}
	}()

	session.Shell()

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
