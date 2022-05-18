package ssh

import (
	"encoding/json"
	"fmt"
	"strings"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/ssh/actions/defaultssh"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	"bastionzero.com/bctl/v1/bzerolib/services/fileservice"
	"bastionzero.com/bctl/v1/bzerolib/services/ioservice"
	"bastionzero.com/bctl/v1/bzerolib/services/tcpservice"
	"bastionzero.com/bctl/v1/bzerolib/services/userservice"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
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

func New(logger *logger.Logger,
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

		switch parsedAction {
		case bzssh.DefaultSsh:
			plugin.action, rerr = defaultssh.New(
				subLogger,
				plugin.doneChan,
				plugin.streamOutputChan,
				synPayload.TargetUser,
				fileservice.OsFileService{},
				ioservice.StdIoService{},
				tcpservice.NetTcpService{},
				userservice.OsUserService{},
			)
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

func (s *SshPlugin) Receive(action string, actionPayload []byte) ([]byte, error) {
	s.logger.Debugf("SSH plugin received message with %s action", action)

	var rerr error
	if payload, err := s.action.Receive(action, actionPayload); err != nil {
		rerr = err
	} else {
		return payload, err
	}

	s.logger.Error(rerr)
	return []byte{}, rerr
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
