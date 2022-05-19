package defaultssh

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	"bastionzero.com/bctl/v1/bzerolib/services/fileservice"
	"bastionzero.com/bctl/v1/bzerolib/services/ioservice"
	"bastionzero.com/bctl/v1/bzerolib/services/tcpservice"
	"bastionzero.com/bctl/v1/bzerolib/services/userservice"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize            = 64 * 1024
	writeDeadline        = 5 * time.Second
	maxKeyLifetime       = 30 * time.Second
	authorizedKeyComment = "bzero-temp-key"
	timeLayout           = "20060102150405"
)

type DefaultSsh struct {
	tmb    tomb.Tomb
	logger *logger.Logger
	closed bool

	// channel for letting the plugin know we're done
	doneChan chan struct{}

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	remoteAddress    *net.TCPAddr
	remoteConnection *net.TCPConn

	targetUser           string
	authorizedKeysFile   string
	currentAuthorizedKey string

	fileService fileservice.FileService
	ioService   ioservice.IoService
	tcpService  tcpservice.TcpService
	userService userservice.UserService
}

func New(
	logger *logger.Logger,
	doneChan chan struct{},
	ch chan smsg.StreamMessage,
	address string,
	port string,
	targetUser string,
	fileService fileservice.FileService,
	ioService ioservice.IoService,
	tcpService tcpservice.TcpService,
	userService userservice.UserService,
) (*DefaultSsh, error) {

	// Open up a connection to the TCP addr we are trying to connect to
	if raddr, err := tcpService.ResolveTCPAddr("tcp", fmt.Sprintf("%s:%s", address, port)); err != nil {
		logger.Errorf("Failed to resolve remote address: %s", err)
		return nil, fmt.Errorf("failed to resolve remote address: %s", err)
	} else {
		action := &DefaultSsh{
			logger:           logger,
			doneChan:         doneChan,
			streamOutputChan: ch,
			remoteAddress:    raddr,
			targetUser:       targetUser,
			fileService:      fileService,
			ioService:        ioService,
			tcpService:       tcpService,
			userService:      userService,
		}

		// as soon as we're born, remove any old entries in our authorized_keys file
		if err := action.removeBzeroKeys(); err != nil {
			action.logger.Errorf("Failed to remove stale entries from /home/%s/.ssh/authorized_keys: %s", targetUser, err)
		}

		return action, nil
	}
}

func (d *DefaultSsh) Kill() {
	d.tmb.Kill(nil)
	if d.remoteConnection != nil {
		(*d.remoteConnection).Close()
	}
	d.tmb.Wait()
}

func (d *DefaultSsh) SetAuthorizedKeysFile() error {
	usr, err := d.userService.Lookup(d.targetUser)
	if err != nil {
		return fmt.Errorf("failed to determine whether user exists: %s", err)
	} else if usr.HomeDir == "" {
		return fmt.Errorf("cannot connect as user without home directorys")
	} else if err := d.fileService.MkdirAll(fmt.Sprintf("%s/.ssh", usr.HomeDir), os.ModePerm); err != nil {
		return fmt.Errorf("failed to create %s/.ssh: %s", usr.HomeDir, err)
	} else {
		d.authorizedKeysFile = filepath.Join(fmt.Sprintf("%s/.ssh", usr.HomeDir), "authorized_keys")
	}

	return nil
}

func (d *DefaultSsh) Receive(action string, actionPayload []byte) ([]byte, error) {
	var err error

	// Update the logger action
	d.logger = d.logger.GetActionLogger(action)
	switch ssh.SshSubAction(action) {
	case ssh.SshOpen:
		var openRequest ssh.SshOpenMessage
		if err = json.Unmarshal(actionPayload, &openRequest); err != nil {
			err = fmt.Errorf("malformed default SSH action payload %v", actionPayload)
			break
		}
		if err = d.handleOpenShellDataAction(openRequest); err != nil {
			break
		}

		// as a security measure, delete the key so that it does not persist in the event of an agent crash
		go func() {
			select {
			case <-d.doneChan:
				return
			case <-d.tmb.Dying():
				return
			case <-time.After(maxKeyLifetime):
				if err := d.removeBzeroKeys(); err != nil {
					d.logger.Errorf("Failed to remove this session's entry from %s's authorized_keys file: %s", d.targetUser, err)
				}
			}
		}()

		return d.start(openRequest, action)
	case ssh.SshInput:

		// Deserialize the action payload, the only action passed is input
		var inputRequest ssh.SshInputMessage
		if err = json.Unmarshal(actionPayload, &inputRequest); err != nil {
			err = fmt.Errorf("unable to unmarshal default SSH input message: %s", err)
			break
		}

		// Set a deadline for the write so we don't block forever
		(*d.remoteConnection).SetWriteDeadline(time.Now().Add(writeDeadline))
		if _, err := (*d.remoteConnection).Write(inputRequest.Data); !d.tmb.Alive() {
			return []byte{}, nil
		} else if err != nil {
			d.logger.Errorf("error writing to local TCP connection: %s", err)
			d.Kill()
		}

	case ssh.SshClose:
		// Deserialize the action payload
		var closeRequest ssh.SshCloseMessage
		if jerr := json.Unmarshal(actionPayload, &closeRequest); jerr != nil {
			// not a fatal error, we can still just close without a reason
			d.logger.Errorf("unable to unmarshal default SSH close message: %s", jerr)
		}

		d.closed = true
		d.logger.Infof("Ending TCP connection because we received this close message from daemon: %s", closeRequest.Reason)
		d.remoteConnection.Close()
		d.Kill()

		return actionPayload, nil
	default:
		err = fmt.Errorf("unhandled stream action: %v", action)
	}

	if err != nil {
		d.logger.Error(err)
	}
	return []byte{}, err
}

func (d *DefaultSsh) start(openRequest ssh.SshOpenMessage, action string) ([]byte, error) {
	d.streamMessageVersion = openRequest.StreamMessageVersion
	d.logger.Debugf("Setting stream message version: %s", d.streamMessageVersion)

	// For each start, call the dial the TCP address
	if remoteConnection, err := d.tcpService.DialTCP("tcp", nil, d.remoteAddress); err != nil {
		return []byte{}, fmt.Errorf("failed to dial remote address: %s", err)
	} else {
		d.remoteConnection = remoteConnection
	}

	// Setup a go routine to listen for messages coming from this local connection and send to daemon
	d.tmb.Go(func() error {
		defer func() {
			close(d.doneChan)
			if err := d.removeBzeroKeys(); err != nil {
				d.logger.Errorf("Failed to remove this session's entry from /home/%s/.ssh/authorized_keys: %s", d.targetUser, err)
			}
		}()

		sequenceNumber := 0
		buff := make([]byte, chunkSize)

		for {
			select {
			case <-d.tmb.Dying():
				d.logger.Errorf("got killed")
				return nil
			default:
				// this line blocks until it reads output or error
				if n, err := (*d.remoteConnection).Read(buff); !d.tmb.Alive() {
					return nil
				} else if err != nil {
					if err == io.EOF {
						d.logger.Errorf("connection closed (EOF)")
						// Let our daemon know that we have got the error and we need to close the connection
						d.sendStreamMessage(sequenceNumber, smsg.StdOut, false, buff[:n])
					} else if !d.closed {
						d.logger.Errorf("failed to read from tcp connection: %s", err)
						d.sendStreamMessage(sequenceNumber, smsg.Error, false, buff[:n])
					}
					// if we're closed, this is an expected error, so we can just end gracefully
					return err
				} else {
					d.logger.Debugf("Sending %d bytes from local tcp connection to daemon", n)

					// Now send this to daemon
					d.sendStreamMessage(sequenceNumber, smsg.StdOut, true, buff[:n])

					sequenceNumber += 1
				}
			}
		}
	})

	// Update our remote connection
	return []byte{}, nil
}

func (d *DefaultSsh) sendStreamMessage(sequenceNumber int, streamType smsg.StreamType, more bool, contentBytes []byte) {
	d.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion:  d.streamMessageVersion,
		SequenceNumber: sequenceNumber,
		Action:         string(ssh.DefaultSsh),
		Type:           streamType,
		More:           more,
		Content:        base64.StdEncoding.EncodeToString(contentBytes),
	}
}

// FIXME: definitely rename this && maybe check publicKey type?
func (d *DefaultSsh) handleOpenShellDataAction(openRequest ssh.SshOpenMessage) error {
	// test that the provided username is valid unix user name
	// source: https://unix.stackexchange.com/a/435120
	usernamePattern := "^[a-z_]([a-z0-9_-]{0,31}|[a-z0-9_-]{0,30}\\$)$"
	var usernameMatch, _ = regexp.MatchString(usernamePattern, d.targetUser)
	if !usernameMatch {
		return fmt.Errorf("invalid username provided: %s", d.targetUser)
	}

	// check if user exists
	if _, err := d.userService.Lookup(d.targetUser); err != nil {
		return fmt.Errorf("failed to find user \"%s\": %s", d.targetUser, err)
	}

	// Construct the authorized key entry
	// Assumes for now only ssh-rsa key types will be generated by the client so we do not need to validate the key type
	keyData := strings.Fields(string(openRequest.PublicKey))
	keyType := keyData[0]
	keyContents := keyData[1]

	// test that the provided public key is valid base64 data
	var _, base64DecodeErr = base64.StdEncoding.DecodeString(string(keyContents))
	if base64DecodeErr != nil {
		return fmt.Errorf("invalid public key provided: %s", keyContents)
	}

	// format the time to the second
	timestamp := time.Now().Format(timeLayout)

	d.currentAuthorizedKey = fmt.Sprintf("%s %s %s created_at=%s", keyType, keyContents, authorizedKeyComment, timestamp)

	// Add an entry to the authorized_keys for the user
	d.logger.Infof("Adding authorized key entry for user: %s", d.targetUser)

	d.SetAuthorizedKeysFile()

	if err := d.fileService.Append(d.authorizedKeysFile, d.currentAuthorizedKey); err != nil {
		return fmt.Errorf("failed to add key to authorized_keys file: %s", err)
	}
	return nil
}

// FIXME: make this threadsafe
// remove bzero-temp-key entries
func (d *DefaultSsh) removeBzeroKeys() error {
	f, err := d.fileService.Open(d.authorizedKeysFile)
	if err != nil {
		return err
	}
	defer f.Close()

	var bs []byte
	buf := bytes.NewBuffer(bs)

	// FIXME: would readfile be safer?
	scanner := d.ioService.NewScanner(f)
	for scanner.Scan() {
		key := scanner.Text()
		keepThisKey := false
		if strings.Contains(key, "created_at=") {
			// any key we find that is younger than 30 seconds should be spared, since the process that wrote it may not have logged in yet
			createdAt, _ := time.Parse(timeLayout, strings.Split(key, "created_at=")[1])
			if time.Since(createdAt) < 30*time.Second {
				keepThisKey = true
			} else {
				continue
			}
		}
		if !strings.Contains(key, authorizedKeyComment) || keepThisKey {
			_, err := buf.Write(scanner.Bytes())
			if err != nil {
				return err
			}
			_, err = buf.WriteString("\n")
			if err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	err = d.fileService.WriteFile(d.authorizedKeysFile, buf.Bytes(), 0666)
	if err != nil {
		return err
	}
	return nil
}
