package shell

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/shell/actions/defaultshell"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"github.com/Masterminds/semver"
)

type IShellAction interface {
	Receive(action string, actionPayload []byte) (string, []byte, error)
	Kill()
}

type ShellPlugin struct {
	logger *logger.Logger

	action           IShellAction
	streamOutputChan chan smsg.StreamMessage
	doneChan         chan struct{}

	runAsUser string

	payloadClean bool
}

func New(
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	action string,
	payload []byte,
	version string,
) (*ShellPlugin, error) {

	// Unmarshal the Syn payload
	var synPayload shell.ShellActionParams
	if err := json.Unmarshal(payload, &synPayload); err != nil {
		return nil, fmt.Errorf("malformed Shell plugin SYN payload %s", string(payload))
	}

	// Create our plugin
	plugin := &ShellPlugin{
		logger:           logger,
		streamOutputChan: ch,
		doneChan:         make(chan struct{}),
		runAsUser:        synPayload.TargetUser,
	}

	if c, err := semver.NewConstraint(">= 2.0"); err != nil {
		return nil, fmt.Errorf("unable to create versioning constraint")
	} else if v, err := semver.NewVersion(version); err != nil {
		return nil, fmt.Errorf("unable to parse version")
	} else {
		plugin.payloadClean = c.Check(v)
	}

	// Start up the action for this plugin
	subLogger := plugin.logger.GetActionLogger(action)
	if parsedAction, err := parseAction(action); err != nil {
		return nil, err
	} else {
		switch parsedAction {
		case shell.DefaultShell:
			if act, err := defaultshell.New(subLogger, plugin.streamOutputChan, plugin.doneChan, plugin.runAsUser); err != nil {
				return nil, fmt.Errorf("could not start new action: %s", err)
			} else {
				plugin.logger.Infof("Shell plugin started %v action", action)
				plugin.action = act
				return plugin, nil
			}
		default:
			return nil, fmt.Errorf("could not start unhandled shell action: %v", action)
		}
	}
}

func (s *ShellPlugin) Receive(action string, actionPayload []byte) (string, []byte, error) {
	s.logger.Debugf("Shell plugin received message with %s action", action)

	if payload, err := s.cleanPayload(actionPayload); err != nil {
		s.logger.Error(err)
		return "", []byte{}, err
	} else if action, payload, err := s.action.Receive(action, payload); err != nil {
		return "", []byte{}, err
	} else {
		return action, payload, err
	}
}

func parseAction(action string) (shell.ShellAction, error) {
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", fmt.Errorf("malformed action: %s", action)
	}
	return shell.ShellAction(parsedAction[1]), nil
}

func (s *ShellPlugin) Done() <-chan struct{} {
	return s.doneChan
}

func (s *ShellPlugin) Kill() {
	if s.action != nil {
		s.action.Kill()
	}
}

func (s *ShellPlugin) cleanPayload(payload []byte) ([]byte, error) {
	if s.payloadClean {
		return payload, nil
	}
	// TODO: The below line removes the extra, surrounding quotation marks that get added at some point in the marshal/unmarshal
	// so it messes up the umarshalling into a valid action payload.  We need to figure out why this is happening
	// so that we can murder its family
	if len(payload) > 0 {
		payload = payload[1 : len(payload)-1]
	}

	// Json unmarshalling encodes bytes in base64
	if payloadSafe, err := base64.StdEncoding.DecodeString(string(payload)); err != nil {
		return []byte{}, fmt.Errorf("error decoding actionPayload: %s", err)
	} else {
		return payloadSafe, nil
	}
}
