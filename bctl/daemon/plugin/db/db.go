package db

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"gopkg.in/tomb.v2"

	agms "bastionzero.com/bctl/v1/bctl/agent/plugin/db"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// Perhaps unnecessary but it is nice to make sure that each action is implementing a common function set
type IDbDaemonAction interface {
	ReceiveKeysplitting(wrappedAction plugin.ActionWrapper)
	ReceiveStream(stream smsg.StreamMessage)
	Start(tmb *tomb.Tomb) error
}

type DbDaemonPlugin struct {
	tmb    *tomb.Tomb
	logger *logger.Logger

	// Input and output channels
	streamInputChan chan smsg.StreamMessage
	outputQueue     chan plugin.ActionWrapper

	// Input and output to communicate with the server
	TcpOuput chan []byte

	// Db-specific vars
	targetHost     string
	targetPort     string
	sequenceNumber int
}

func New(parentTmb *tomb.Tomb, logger *logger.Logger, actionParams agms.DbActionParams) (*DbDaemonPlugin, error) {
	plugin := DbDaemonPlugin{
		tmb:             parentTmb,
		logger:          logger,
		streamInputChan: make(chan smsg.StreamMessage, 25),
		sequenceNumber:  0,
		outputQueue:     make(chan plugin.ActionWrapper, 25),
		targetHost:      "",
		targetPort:      "",
		TcpOuput:        make(chan []byte, 25),
	}

	// listener for processing any incoming stream messages, since they are not treated as part of
	// the keysplitting syncronous chain
	go func() {
		for {
			select {
			case <-plugin.tmb.Dying():
				return
			case streamMessage := <-plugin.streamInputChan:
				plugin.processStream(streamMessage)
			}
		}
	}()

	return &plugin, nil
}

func (k *DbDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	k.streamInputChan <- smessage
}

func (k *DbDaemonPlugin) processStream(smessage smsg.StreamMessage) error {
	// check for end of stream
	contentBytes, _ := base64.StdEncoding.DecodeString(smessage.Content)

	// Send the strem message to our tcpoutput channel so our server can pick it up
	k.TcpOuput <- contentBytes

	return nil
}

func (k *DbDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) (string, []byte, error) {
	// First, process the incoming message
	if err := k.processKeysplitting(action, actionPayload); err != nil {
		return "", []byte{}, err
	}

	// Now that we've received, we wait for any new outgoing commands.  Because the existence of any
	// such command is dependent on the user, there may not be one waiting so we wait for it.
	k.logger.Info("Waiting for input...")

	select {
	case <-k.tmb.Dying():
		return "", []byte{}, nil
	case actionMessage := <-k.outputQueue: // some action's got something to say
		k.logger.Infof("Sending input from action: %v", actionMessage.Action)

		// turn the actionPayload into bytes and return it
		if actionPayloadBytes, err := json.Marshal(actionMessage.ActionPayload); err != nil {
			k.logger.Infof("actionPayload: %+v", actionPayload)
			return "", []byte{}, fmt.Errorf("could not marshal actionPayload json: %s", err)
		} else {
			return actionMessage.Action, actionPayloadBytes, nil
		}
	}
}

func (k *DbDaemonPlugin) processKeysplitting(action string, actionPayload []byte) error {
	// if actionPayload is empty, then there's nothing we need to process
	if len(actionPayload) == 0 {
		return nil
	}

	return nil
}

func (k *DbDaemonPlugin) Feed(action string, data []byte) error {
	switch agms.DbAction(action) {
	case agms.DataIn:
		// Package and forward this request to bastion
		// Encode the bytes first
		dataToSend := base64.StdEncoding.EncodeToString(data)
		payload := agms.DbDataInActionPayload{
			Data:           dataToSend,
			SequenceNumber: k.sequenceNumber,
		}

		// send action payload to plugin to be sent to agent
		payloadBytes, _ := json.Marshal(payload)
		k.outputQueue <- plugin.ActionWrapper{
			Action:        string(agms.DataIn),
			ActionPayload: payloadBytes,
		}

		// Increment sequence number
		k.sequenceNumber += 1
	default:
		rerr := fmt.Errorf("unrecognized db action: %v", string(action))
		k.logger.Error(rerr)
		return rerr
	}
	return nil
}
