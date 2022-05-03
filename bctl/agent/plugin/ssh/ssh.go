package ssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/ssh/actions/defaultssh"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

type ISshAction interface {
	Receive(action string, actionPayload []byte) (string, []byte, error)
	Closed() bool
}

type SshPlugin struct {
	logger *logger.Logger
	tmb    *tomb.Tomb // datachannel's tomb

	action           ISshAction
	streamOutputChan chan smsg.StreamMessage

	// specific to ssh
	targetUser string
}

func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	action string,
	payload []byte) (*SshPlugin, error) {

	// Unmarshal the Syn payload
	var synPayload bzssh.SshActionParams
	if err := json.Unmarshal(payload, &synPayload); err != nil {
		return nil, fmt.Errorf("malformed SSH plugin SYN payload %v", string(payload))
	}

	// Create our plugin
	plugin := &SshPlugin{
		targetUser:       synPayload.TargetUser,
		logger:           logger,
		tmb:              parentTmb, // if datachannel dies, so should we
		streamOutputChan: ch,
	}

	// Start up the action for this plugin
	subLogger := plugin.logger.GetActionLogger(action)
	if parsedAction, err := parseAction(action); err != nil {
		return nil, err
	} else {
		var rerr error

		switch parsedAction {
		case bzssh.DefaultSsh:
			plugin.action, rerr = defaultssh.New(subLogger, plugin.tmb, plugin.streamOutputChan)
		default:
			rerr = fmt.Errorf("unhandled SSH action")
		}

		if rerr != nil {
			return nil, fmt.Errorf("failed to start SSH plugin with action %s: %s", action, rerr)
		} else {
			plugin.logger.Infof("SSH plugin started with %v action", action)
			return plugin, nil
		}
	}
}

func (s *SshPlugin) Receive(action string, actionPayload []byte) (string, []byte, error) {
	s.logger.Debugf("SSH plugin received message with %s action", action)

	var rerr error
	if safePayload, err := cleanPayload(actionPayload); err != nil {
		rerr = err
	} else if action, payload, err := s.action.Receive(action, safePayload); err != nil {
		rerr = err
	} else {
		return action, payload, err
	}

	s.logger.Error(rerr)
	return "", []byte{}, rerr
}

func parseAction(action string) (bzssh.SshAction, error) {
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", fmt.Errorf("malformed action: %s", action)
	}
	return bzssh.SshAction(parsedAction[1]), nil
}

func cleanPayload(payload []byte) ([]byte, error) {
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
