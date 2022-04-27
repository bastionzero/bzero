package shell

import (
	"fmt"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/shell/actions/defaultshell"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type IShellAction interface {
	ReceiveStream(stream smsg.StreamMessage)
	Start(attach bool) error
	Replay(replayData []byte) error
	Done() <-chan struct{}
	Kill()
}

type ShellDaemonPlugin struct {
	logger *logger.Logger

	outputQueue chan bzplugin.ActionWrapper

	action IShellAction
}

func New(logger *logger.Logger) *ShellDaemonPlugin {
	return &ShellDaemonPlugin{
		logger:      logger,
		outputQueue: make(chan bzplugin.ActionWrapper, 10),
	}
}

func (s *ShellDaemonPlugin) StartAction(attach bool) error {
	// Create the DefaultShell action
	actLogger := s.logger.GetActionLogger(string(bzshell.DefaultShell))
	s.action = defaultshell.New(actLogger, s.outputQueue)

	// Start the shell action
	if err := s.action.Start(attach); err != nil {
		return fmt.Errorf("error starting the shell action: %s", err)
	} else {
		return nil
	}
}

func (s *ShellDaemonPlugin) Kill() {
	if s.action != nil {
		s.action.Kill()
	}
}

func (s *ShellDaemonPlugin) Done() <-chan struct{} {
	if s.action != nil {
		return s.action.Done()
	} else {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
}

func (s *ShellDaemonPlugin) Outbox() <-chan bzplugin.ActionWrapper {
	return s.outputQueue
}

func (s *ShellDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	s.logger.Debugf("shell plugin received %v stream", smessage.Type)
	if s.action != nil {
		s.action.ReceiveStream(smessage)
	} else {
		s.logger.Debug("shell plugin received stream message before an action was created. Ignoring")
	}
}

func (s *ShellDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) error {
	s.logger.Infof("Shell plugin received keysplitting message with action: %s", action)

	switch action {
	case string(bzshell.ShellReplay):
		return s.action.Replay(actionPayload)
	default:
		return nil
	}
}
