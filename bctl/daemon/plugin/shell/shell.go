package shellplugin

import (
	"encoding/json"
	"fmt"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/shell/actions/defaultshell"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

type IShellAction interface {
	ReceiveStream(stream smsg.StreamMessage)
	Start(tmb *tomb.Tomb, attach bool) error
	Replay(replayData []byte) error
}

type ShellDaemonPlugin struct {
	tmb    *tomb.Tomb
	logger *logger.Logger

	// Input and output channels
	streamInputChan chan smsg.StreamMessage
	outputQueue     chan plugin.ActionWrapper

	action IShellAction
	attach bool
}

func New(parentTmb *tomb.Tomb, logger *logger.Logger, actionParams bzshell.ShellActionParams, attach bool) (*ShellDaemonPlugin, error) {
	shellDaemonPlugin := ShellDaemonPlugin{
		tmb:             parentTmb,
		logger:          logger,
		streamInputChan: make(chan smsg.StreamMessage, 25),
		outputQueue:     make(chan plugin.ActionWrapper, 25),
		attach:          attach,
	}

	// listener for processing any incoming stream messages, since they are not treated as part of
	// the keysplitting synchronous chain
	go func() {
		for {
			select {
			case <-shellDaemonPlugin.tmb.Dying():
				return
			case streamMessage := <-shellDaemonPlugin.streamInputChan:
				if err := shellDaemonPlugin.processStream(streamMessage); err != nil {
					shellDaemonPlugin.logger.Error(err)
				}
			}
		}
	}()

	// Create the DefaultShell action
	actLogger := logger.GetActionLogger(string(bzshell.DefaultShell))
	var actOutputChan chan plugin.ActionWrapper
	shellDaemonPlugin.action, actOutputChan = defaultshell.New(actLogger)

	// listen to shell action output channel and push to outputqueue. If output
	// channel is done then close the shell daemon plugin
	go func() {
		for {
			select {
			case <-shellDaemonPlugin.tmb.Dying():
				return
			case m, more := <-actOutputChan:
				logger.Infof("GOING TO OUTPUT CHAN")
				if more {
					shellDaemonPlugin.outputQueue <- m
				} else {
					shellDaemonPlugin.logger.Infof("Closing shell plugin")
					shellDaemonPlugin.tmb.Kill(fmt.Errorf("done with the only action this datachannel will ever do"))
					return
				}
			}
		}
	}()

	// Start the shell action
	if err := shellDaemonPlugin.action.Start(shellDaemonPlugin.tmb, attach); err != nil {
		return &shellDaemonPlugin, fmt.Errorf("error starting the shell action: %s", err)
	}

	return &shellDaemonPlugin, nil
}

func (s *ShellDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	s.logger.Debugf("shell plugin received %v stream", smessage.Type)
	s.streamInputChan <- smessage
}

func (d *ShellDaemonPlugin) processStream(smessage smsg.StreamMessage) error {
	if d.action != nil {
		d.action.ReceiveStream(smessage)
		return nil
	} else {
		return fmt.Errorf("shell plugin received stream message before an action was created. Ignoring")
	}
}

func (s *ShellDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) (string, []byte, error) {
	s.logger.Infof("Received a keysplitting message with action: %s", action)
	// First, process the incoming message
	if err := s.processKeysplitting(action, actionPayload); err != nil {
		return "", []byte{}, err
	}

	// Now that we've received, we wait for any new outgoing commands.  Because the existence of any
	// such command is dependent on the user, there may not be one waiting so we wait for it.
	s.logger.Info("Waiting for input...")

	select {
	case <-s.tmb.Dying():
		return "", []byte{}, nil
	case actionMessage := <-s.outputQueue: // some action's got something to say
		s.logger.Infof("Sending input from action: %v", actionMessage.Action)

		// turn the actionPayload into bytes and return it
		if actionPayloadBytes, err := json.Marshal(actionMessage.ActionPayload); err != nil {
			s.logger.Infof("actionPayload: %+v", actionPayload)
			return "", []byte{}, fmt.Errorf("could not marshal actionPayload json: %s", err)
		} else {
			return actionMessage.Action, actionPayloadBytes, nil
		}
	}
}

func (s *ShellDaemonPlugin) processKeysplitting(action string, actionPayload []byte) error {
	s.logger.Infof("Shell plugin received keysplitting message with action: %s", action)

	switch action {
	case string(shell.ShellReplay):
		return s.action.Replay(actionPayload)
	default:
		return nil
	}
}

func (s *ShellDaemonPlugin) Feed(food interface{}) error {
	return nil
}
