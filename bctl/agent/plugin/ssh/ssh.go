package ssh

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/ssh/actions/opaquessh"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/ssh/actions/transparentssh"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/ssh/authorizedkeys"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"bastionzero.com/bctl/v1/bzerolib/unix/unixuser"
)

const (
	sshFolder      = ".ssh"
	maxKeyLifetime = 30 * time.Second
)

type ISshAction interface {
	Receive(action string, actionPayload []byte) ([]byte, error)
	Kill()
}

type SshPlugin struct {
	logger *logger.Logger

	action           ISshAction
	streamOutputChan chan smsg.StreamMessage

	doneChan chan struct{}
}

func New(logger *logger.Logger, ch chan smsg.StreamMessage, action string, payload []byte) (*SshPlugin, error) {

	// Unmarshal the Syn payload
	var synPayload bzssh.SshActionParams
	if err := json.Unmarshal(payload, &synPayload); err != nil {
		return nil, fmt.Errorf("malformed Ssh plugin SYN payload %v", string(payload))
	}

	// Create our plugin
	plugin := &SshPlugin{
		logger:           logger,
		streamOutputChan: ch,
		doneChan:         make(chan struct{}),
	}

	// Start up the action for this plugin
	subLogger := plugin.logger.GetActionLogger(action)
	if parsedAction, err := parseAction(action); err != nil {
		return nil, err
	} else {
		var rerr error

		subSubLogger := subLogger.GetComponentLogger("authorized_keys")

		// Create will create the user with the given username if it is allowed, or it will return the existing user
		usr, err := unixuser.LookupOrCreateFromList(synPayload.TargetUser)
		if err != nil {
			rerr = fmt.Errorf("failed to use ssh as user %s: %s", synPayload.TargetUser, err)
		}

		// we place the authorized keys lock file inside the user's /home/.ssh/ directory because that is the least bad place for it
		// source: https://i.stack.imgur.com/BlpRb.png
		authKeys, err := authorizedkeys.New(subSubLogger, plugin.doneChan, usr, sshFolder, sshFolder, maxKeyLifetime)
		if err != nil {
			rerr = fmt.Errorf("failed to set up authorized_keys file: %s", err)
		}

		if rerr == nil {
			remoteAddress := fmt.Sprintf("%s:%d", synPayload.RemoteHost, synPayload.RemotePort)
			switch parsedAction {
			case bzssh.OpaqueSsh:
				// Open up a connection to the TCP addr we are trying to connect to
				raddr, err := net.ResolveTCPAddr("tcp", remoteAddress)
				if err != nil {
					rerr = fmt.Errorf("failed to resolve remote address: %s", err)
					break
				}
				remoteConnection, err := net.DialTCP("tcp", nil, raddr)
				if err != nil {
					rerr = fmt.Errorf("failed to dial remote address: %s", err)
					break
				}

				plugin.action = opaquessh.New(
					subLogger,
					plugin.doneChan,
					plugin.streamOutputChan,
					remoteConnection,
					authKeys,
				)

			case bzssh.TransparentSsh:
				plugin.action = transparentssh.New(
					subLogger,
					plugin.doneChan,
					plugin.streamOutputChan,
					authKeys,
					synPayload.TargetUser,
					remoteAddress,
				)

			default:
				rerr = fmt.Errorf("unhandled Ssh action %s", parsedAction)
			}
		}

		if rerr != nil {
			return nil, fmt.Errorf("failed to start Ssh plugin with action %s: %s", action, rerr)
		} else {
			plugin.logger.Infof("Ssh plugin started with %v action", action)
			return plugin, nil
		}
	}
}

func (s *SshPlugin) Receive(action string, actionPayload []byte) ([]byte, error) {
	s.logger.Debugf("Ssh plugin received message with %s action", action)

	if payload, err := s.action.Receive(action, actionPayload); err != nil {
		s.logger.Error(err)
		return []byte{}, err
	} else {
		return payload, nil
	}
}

func (s *SshPlugin) Done() <-chan struct{} {
	return s.doneChan
}

func (s *SshPlugin) Kill() {
	if s.action != nil {
		s.action.Kill()
	}
}

func parseAction(action string) (bzssh.SshAction, error) {
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", fmt.Errorf("malformed action: %s", action)
	}
	return bzssh.SshAction(parsedAction[1]), nil
}
