package defaultssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize = 64 * 1024

	ShellPluginCommandName     = "sh"
	ShellPluginBashCommandName = "/bin/bash"
)

var ShellPluginCommandArgs = []string{"-c"}

type DefaultSsh struct {
	logger *logger.Logger
	tmb    *tomb.Tomb
	closed bool

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	remoteAddress *net.TCPAddr

	remoteConnection *net.TCPConn

	authorizedKeyEntry string
}

func New(logger *logger.Logger, pluginTmb *tomb.Tomb, ch chan smsg.StreamMessage) (*DefaultSsh, error) {

	// Open up a connection to the TCP addr we are trying to connect to
	// TODO: const?
	if raddr, err := net.ResolveTCPAddr("tcp", "localhost:22"); err != nil {
		logger.Errorf("Failed to resolve remote address: %s", err)
		return nil, fmt.Errorf("failed to resolve remote address: %s", err)
	} else {
		return &DefaultSsh{
			logger:           logger,
			tmb:              pluginTmb,
			closed:           false,
			streamOutputChan: ch,
			remoteAddress:    raddr,
		}, nil
	}
}

func (d *DefaultSsh) Closed() bool {
	return d.closed
}

func (d *DefaultSsh) Receive(action string, actionPayload []byte) (string, []byte, error) {
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
		d.handleOpenShellDataAction(openRequest)

		return d.start(openRequest, action)
	case ssh.SshInput:

		// Deserialize the action payload, the only action passed is input
		var inputRequest ssh.SshInputMessage
		if err = json.Unmarshal(actionPayload, &inputRequest); err != nil {
			err = fmt.Errorf("unable to unmarshal default SSH input message: %s", err)
			break
		}

		// Send this data to our remote connection
		d.logger.Info("Received data from daemon, forwarding to remote tcp connection")
		_, err = d.remoteConnection.Write(inputRequest.Data)

	case ssh.SshClose:

		// Deserialize the action payload
		var closeRequest ssh.SshCloseMessage
		if jerr := json.Unmarshal(actionPayload, &closeRequest); jerr != nil {
			err = fmt.Errorf("unable to unmarshal default SSH input message: %s", jerr)
			break
		}

		d.closed = true // Ensure that we close the dial action
		d.remoteConnection.Close()

		// give our streamoutputchan time to process all the messages we sent while the stop request was getting here
		return action, actionPayload, nil
	default:
		err = fmt.Errorf("unhandled stream action: %v", action)
	}

	if err != nil {
		d.logger.Error(err)
	}
	return "", []byte{}, err
}

func (d *DefaultSsh) start(openRequest ssh.SshOpenMessage, action string) (string, []byte, error) {

	d.logger.Errorf("oh hi mark it's %s", openRequest.PublicKey)
	// FIXME: add this back in
	//d.streamMessageVersion = openRequest.StreamMessageVersion
	//d.logger.Infof("Setting stream message version: %s", d.streamMessageVersion)

	// For each start, call the dial the TCP address
	if remoteConnection, err := net.DialTCP("tcp", nil, d.remoteAddress); err != nil {
		d.logger.Errorf("Failed to dial remote address: %s", err)
		return action, []byte{}, err
	} else {
		d.remoteConnection = remoteConnection
	}

	// set up a go routine to listen to the tomb dying so that we can interrupt our listening routine
	go func() {
		<-d.tmb.Dying()
		d.remoteConnection.Close()
	}()

	// Setup a go routine to listen for messages coming from this local connection and send to daemon
	go func() {
		defer d.remoteConnection.Close()
		defer func() {
			d.closed = true
		}()

		sequenceNumber := 0
		buff := make([]byte, chunkSize)

		for {
			// this line blocks until it reads output or error
			n, err := d.remoteConnection.Read(buff)

			if d.closed {
				return
			}

			if err != nil {
				if err != io.EOF {
					d.logger.Errorf("failed to read from tcp connection: %s", err)
				} else {
					d.logger.Errorf("connection closed")
				}

				// Let our daemon know that we have got the error and we need to close the connection
				d.sendStreamMessage(sequenceNumber, smsg.Error, false, buff[:n])
				return
			}

			d.logger.Debugf("Sending %d bytes from local tcp connection to daemon", n)

			// Now send this to daemon
			d.sendStreamMessage(sequenceNumber, smsg.StdOut, true, buff[:n])

			sequenceNumber += 1
		}
	}()

	// Update our remote connection
	return action, []byte{}, nil
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

// FIXME: check publicKey type
func (d *DefaultSsh) handleOpenShellDataAction(openRequest ssh.SshOpenMessage) error {

	// test that the provided username is valid unix user name
	// source: https://unix.stackexchange.com/a/435120
	usernamePattern := "^[a-z_]([a-z0-9_-]{0,31}|[a-z0-9_-]{0,30}\\$)$"
	var usernameMatch, _ = regexp.MatchString(usernamePattern, openRequest.TargetUser)
	if !usernameMatch {
		return fmt.Errorf("invalid username provided: %s", openRequest.TargetUser)
	}

	// test that the provided public key is valid base64 data
	var _, base64DecodeErr = base64.StdEncoding.DecodeString(string(openRequest.PublicKey))
	if base64DecodeErr != nil {
		return fmt.Errorf("invalid public key provided: %s", openRequest.PublicKey)
	}

	// Construct the authorized key entry with a custom comment
	// Assumes for now only ssh-rsa key types will be generated by the client so we do not need to validate the key type
	keyType := "ssh-rsa"
	comment := "bzero-temp-key"
	d.authorizedKeyEntry = fmt.Sprintf("%s %s %s", keyType, openRequest.PublicKey, comment)

	/* FIXME: may or may not need this
	// Check the user exists
	u := &utility.SessionUtil{}
	var userExists, _ = u.DoesUserExist(sshOpenActionPayload.Username)
	if !userExists {
		return fmt.Errorf("%s user doesnt exist", sshOpenActionPayload.Username)
	}
	*/

	// Add an entry to the authorized_keys for the user
	d.logger.Infof("Adding authorized key entry %s for user: %s", d.authorizedKeyEntry, openRequest.TargetUser)
	var keyAdded, err = addToAuthorizedKeyFile(openRequest.TargetUser, d.authorizedKeyEntry)
	if !keyAdded {
		d.logger.Errorf("failed to add authorized key entry for user %s: %v", openRequest.TargetUser, err)
		return fmt.Errorf("failed to add temporary authorized ssh key")
	}

	// Save a reference to the sshOpenActionPayload so we can check when
	// stopping the port plugin if we need to remove an authorized key or not
	//d.sshOpenData = &sshOpenActionPayload

	return nil
}

// Appends an authorized key entry to the authorized_keys file within username's .ssh directory
func addToAuthorizedKeyFile(username string, authorizedKey string) (bool, error) {
	// make a .ssh directory for the user if it doesnt exist and then append the authorizedKey string to the authorized_keys file within the .ssh directory
	authorizedKeyFile := fmt.Sprintf("~%s/.ssh/authorized_keys", username)
	sshFolder := fmt.Sprintf("~%s/.ssh", username)
	createSshDirectory := fmt.Sprintf("mkdir -p %s", sshFolder)
	shellCmdArgsCreateSshDirectory := append(ShellPluginCommandArgs, createSshDirectory)
	cmdCreateSshDirectory := exec.Command(ShellPluginBashCommandName, shellCmdArgsCreateSshDirectory...)
	if err := cmdCreateSshDirectory.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// The program has exited with an exit code != 0
			return false, fmt.Errorf("error executing %s %v", cmdCreateSshDirectory.Args, exitErr.Error())
		}
		return false, nil
	}

	appendKeyCmd := fmt.Sprintf("echo '%s' >> %s", authorizedKey, authorizedKeyFile)
	shellCmdArgsAppendKey := append(ShellPluginCommandArgs, lockFolderAndRunCommand(authorizedKeyFile, appendKeyCmd))
	cmdAppendKey := exec.Command(ShellPluginBashCommandName, shellCmdArgsAppendKey...)
	if err := cmdAppendKey.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// The program has exited with an exit code != 0
			return false, fmt.Errorf("error executing %s %v", cmdAppendKey.Args, exitErr.Error())
		}
		return false, nil
	}
	return true, nil
}

// Use flock to wait for exclusive lock on fileToLock for up to 10 seconds (or exit) and then run a command
func lockFolderAndRunCommand(folderToLock string, commandToRun string) string {
	return fmt.Sprintf("flock -x -w 10 %s -c \"%s\"", folderToLock, commandToRun)
}
