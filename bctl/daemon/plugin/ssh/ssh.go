package ssh

import (
	"fmt"
	"net"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/ssh/actions/opaquessh"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/ssh/actions/transparentssh"
	"bastionzero.com/bctl/v1/bzerolib/bzio"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// Perhaps unnecessary but it is nice to make sure that each action is implementing a common function set
type ISshAction interface {
	ReceiveStream(stream smsg.StreamMessage)
	Start() error
	Kill()
}

type SshDaemonPlugin struct {
	logger       *logger.Logger
	outboxQueue  chan bzplugin.ActionWrapper
	doneChan     chan struct{}
	killed       bool
	action       ISshAction
	localPort    string
	identityFile bzssh.IIdentityFile
	knownHosts   bzssh.IKnownHosts
	stdIo        bzio.BzIo
}

func New(logger *logger.Logger, localPort string, identityFile bzssh.IIdentityFile, knownHosts bzssh.IKnownHosts, stdIo bzio.StdIo) *SshDaemonPlugin {
	return &SshDaemonPlugin{
		logger:       logger,
		outboxQueue:  make(chan bzplugin.ActionWrapper, 10),
		doneChan:     make(chan struct{}),
		killed:       false,
		localPort:    localPort,
		identityFile: identityFile,
		knownHosts:   knownHosts,
		stdIo:        stdIo,
	}
}

func (s *SshDaemonPlugin) StartAction(actionName string) error {
	if s.killed {
		return fmt.Errorf("plugin has already been killed, cannot create a new ssh action")
	}

	// Create the action
	actLogger := s.logger.GetActionLogger(actionName)
	switch actionName {
	case string(bzssh.OpaqueSsh):
		// FIXME: add args
		s.action = opaquessh.New(actLogger, s.outboxQueue, s.doneChan, s.stdIo, s.identityFile, s.knownHosts)
	case string(bzssh.TransparentSsh):
		// listen for a connection from the ZLI
		// action is responsible for closing this
		listener, err := net.Listen("tcp", fmt.Sprintf(":%s", s.localPort))
		if err != nil {
			s.logger.Errorf("failed to listen for connection: %s", err)
		}
		s.action = transparentssh.New(actLogger, s.outboxQueue, s.doneChan, s.stdIo, listener, s.identityFile, s.knownHosts)
	}

	// Start the ssh action
	if err := s.action.Start(); err != nil {
		return fmt.Errorf("error starting the ssh action: %s", err)
	} else {
		return nil
	}
}

func (s *SshDaemonPlugin) Kill() {
	s.killed = true
	if s.action != nil {
		s.action.Kill()
	}
}

func (s *SshDaemonPlugin) Done() <-chan struct{} {
	return s.doneChan
}

func (s *SshDaemonPlugin) Outbox() <-chan bzplugin.ActionWrapper {
	return s.outboxQueue
}

func (s *SshDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	s.logger.Debugf("Ssh plugin received %v stream", smessage.Type)
	if s.action != nil {
		s.action.ReceiveStream(smessage)
	} else {
		s.logger.Debug("Ssh plugin received stream message before an action was created. Ignoring")
	}
}

func (s *SshDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) error {
	return nil
}
