package ssh

import (
	"encoding/json"
	"fmt"
	"net"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/ssh/actions/defaultssh"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// Perhaps unnecessary but it is nice to make sure that each action is implementing a common function set
type ISshDaemonAction interface {
	ReceiveKeysplitting(wrappedAction plugin.ActionWrapper)
	ReceiveStream(stream smsg.StreamMessage)
	Start(tmb *tomb.Tomb, lconn *net.TCPConn) error
}

type SshDaemonPlugin struct {
	tmb    *tomb.Tomb
	logger *logger.Logger

	// Input and output channels
	streamInputChan chan smsg.StreamMessage
	outputQueue     chan plugin.ActionWrapper

	action ISshDaemonAction

	// TODO: remove?
	// Db-specific vars
	//sequenceNumber int
}

func New(parentTmb *tomb.Tomb, logger *logger.Logger, actionParams bzssh.SshActionParams) (*SshDaemonPlugin, error) {
	sshDaemonPlugin := SshDaemonPlugin{
		tmb:             parentTmb,
		logger:          logger,
		streamInputChan: make(chan smsg.StreamMessage, 25),
		outputQueue:     make(chan plugin.ActionWrapper, 25),
	}

	// listener for processing any incoming stream messages, since they are not treated as part of
	// the keysplitting synchronous chain
	go func() {
		for {
			select {
			case <-sshDaemonPlugin.tmb.Dying():
				return
			case streamMessage := <-sshDaemonPlugin.streamInputChan:
				if err := sshDaemonPlugin.processStream(streamMessage); err != nil {
					sshDaemonPlugin.logger.Error(err)
				}
			}
		}
	}()

	// Create the DefaultSsh action
	actLogger := logger.GetActionLogger(string(bzssh.DefaultSsh))
	var actOutputChan chan plugin.ActionWrapper
	// FIXME: Help! I need an action
	sshDaemonPlugin.action, actOutputChan = defaultssh.New(actLogger)

	// listen to ssh action output channel and push to outputqueue. If output
	// channel is done then close the ssh daemon plugin
	go func() {
		for {
			select {
			case <-sshDaemonPlugin.tmb.Dying():
				return
			case m, more := <-actOutputChan:
				if more {
					sshDaemonPlugin.outputQueue <- m
				} else {
					sshDaemonPlugin.logger.Infof("Closing ssh plugin")
					sshDaemonPlugin.tmb.Kill(fmt.Errorf("done with the only action this datachannel will ever do"))
					return
				}
			}
		}
	}()

	// Start the ssh action
	// FIXME: Help! I need a connection
	if err := sshDaemonPlugin.action.Start(sshDaemonPlugin.tmb, &net.TCPConn{}); err != nil {
		return &sshDaemonPlugin, fmt.Errorf("error starting the ssh action: %s", err)
	}

	return &sshDaemonPlugin, nil
}

func (s *SshDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	s.logger.Debugf("Ssh plugin received %v stream", smessage.Type)
	s.streamInputChan <- smessage
}

func (s *SshDaemonPlugin) processStream(smessage smsg.StreamMessage) error {
	if s.action != nil {
		s.action.ReceiveStream(smessage)
		return nil
	} else {
		return fmt.Errorf("Ssh plugin received stream message before an action was created. Ignoring")
	}
}

func (s *SshDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) (string, []byte, error) {
	s.logger.Infof("Ssh plugin received a keysplitting message with action: %s", action)
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

func (s *SshDaemonPlugin) processKeysplitting(action string, actionPayload []byte) error {
	s.logger.Infof("Ssh plugin received keysplitting message with action: %s", action)

	// currently the only keysplitting message we care about is the acknowledgement of our request for the agent to stop the dial action
	// but since we don't do anything with it we log it, make a comment, and return nil
	return nil
}

func (s *SshDaemonPlugin) Feed(food interface{}) error {
	return nil
}
