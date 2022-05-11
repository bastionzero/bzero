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
	Kill()
}

type ShellDaemonPlugin struct {
	logger      *logger.Logger
	outboxQueue chan bzplugin.ActionWrapper
	doneChan    chan struct{}
	killed      bool
	action      IShellAction
}

func New(logger *logger.Logger) *ShellDaemonPlugin {
	return &ShellDaemonPlugin{
		logger:      logger,
		outboxQueue: make(chan bzplugin.ActionWrapper, 10),
		doneChan:    make(chan struct{}),
		killed:      false,
	}
}

func (s *ShellDaemonPlugin) StartAction(attach bool) error {
	if s.killed {
		return fmt.Errorf("plugin has already been killed, cannot create a new shell action")
	}

	// Create the DefaultShell action
	actLogger := s.logger.GetActionLogger(string(bzshell.DefaultShell))
	s.action = defaultshell.New(actLogger, s.outboxQueue, s.doneChan)

	// Start the shell action
	if err := s.action.Start(attach); err != nil {
		return fmt.Errorf("error starting the shell action: %s", err)
	} else {
		return nil
	}
}

func (s *ShellDaemonPlugin) Kill() {
	s.killed = true
	if s.action != nil {
		s.action.Kill()
	}
}

func (s *ShellDaemonPlugin) Done() <-chan struct{} {
	return s.doneChan
}

func (s *ShellDaemonPlugin) Outbox() <-chan bzplugin.ActionWrapper {
	return s.outboxQueue
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
