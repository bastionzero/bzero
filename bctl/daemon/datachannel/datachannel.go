package datachannel

import (
	"encoding/json"
	"fmt"
	"time"

	tomb "gopkg.in/tomb.v2"

	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	rrr "bastionzero.com/bctl/v1/bzerolib/error"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	// amount of time we're willing to wait for our first keysplitting message
	handshakeTimeout = time.Minute // TODO: Decrease

	// maximum amount of time we want to keep this datachannel alive after
	// neither receiving nor sending anything
	datachannelIdleTimeout = 7 * 24 * time.Hour
)

type OpenDataChannelPayload struct {
	Syn    []byte `json:"syn"`
	Action string `json:"action"`
}

type IKeysplitting interface {
	BuildSyn(action string, payload interface{}, send bool) (*ksmsg.KeysplittingMessage, error)
	Validate(ksMessage *ksmsg.KeysplittingMessage) error
	Recover(errorMessage rrr.ErrorMessage) error
	Inbox(action string, actionPayload []byte) error
	IsPipelineEmpty() bool
	Outbox() <-chan *ksmsg.KeysplittingMessage
	Release()
	Recovering() bool
}

type IPlugin interface {
	ReceiveKeysplitting(action string, actionPayload []byte) error
	ReceiveStream(smessage smsg.StreamMessage)
	Outbox() <-chan bzplugin.ActionWrapper
	Done() <-chan struct{}
	Kill()
}

type DataChannel struct {
	tmb    tomb.Tomb
	logger *logger.Logger
	id     string // DataChannel's ID

	websocket   websocket.IWebsocket
	keysplitter IKeysplitting
	plugin      IPlugin

	// channels for incoming messages
	inputChan chan *am.AgentMessage

	// whether or not to wait for the inputChannel queue to be flushed before exiting
	processInputChanBeforeExit bool
}

func New(
	logger *logger.Logger,
	id string,
	parentTmb *tomb.Tomb, // daemon has ability to rage quit and take everything down with it
	websocket websocket.IWebsocket,
	keysplitter IKeysplitting,
	plugin IPlugin,
	action string,
	synPayload interface{},
	attach bool, // bool to indicate if we are attaching to an existing datachannel
	processInputChanBeforeExit bool,
) (*DataChannel, *tomb.Tomb, error) {

	dc := &DataChannel{
		logger:                     logger,
		id:                         id,
		websocket:                  websocket,
		keysplitter:                keysplitter,
		plugin:                     plugin,
		inputChan:                  make(chan *am.AgentMessage, 50),
		processInputChanBeforeExit: processInputChanBeforeExit,
	}

	// register with websocket so datachannel can send and receive messages
	websocket.Subscribe(id, dc)

	// if we're attaching to an existing datachannel vs if we are creating a new one
	if !attach {
		// tell Bastion we're opening a datachannel and send SYN to agent initiates an authenticated datachannel
		logger.Info("Sending request to agent to open a new datachannel")
		if err := dc.openDataChannel(action, synPayload); err != nil {
			return nil, nil, err
		}
	} else if _, err := keysplitter.BuildSyn(action, synPayload, true); err != nil {
		return nil, nil, fmt.Errorf("failed to build and send syn for attachment flow")
	} else {
		logger.Infof("Sending SYN on existing datachannel %s with actions %s.", dc.id, action)
	}

	dc.tmb.Go(func() error {
		defer websocket.Unsubscribe(id) // causes decoupling from websocket
		defer dc.logger.Info("Datachannel done")

		// wait for the syn/ack to our intial syn message or an error
		if err := dc.handshakeOrTimeout(); err != nil {
			dc.logger.Error(err)
			return err
		}
		dc.logger.Info("Initial handshake complete")

		for {
			select {
			case <-parentTmb.Dying(): // daemon is dying
				dc.logger.Info("Datachannel was orphaned too young and can't be batman :'(")
				dc.plugin.Kill()
				return nil
			case <-dc.tmb.Dying():
				dc.logger.Infof("Datachannel dying: %s", dc.tmb.Err().Error())
				dc.plugin.Kill()
				return nil
			case <-dc.plugin.Done():
				dc.logger.Infof("%s is done", action)
				if processInputChanBeforeExit {
					// wait for any in-flight messages to come in and ensure all outgoing messages go out
					return dc.waitForRemainingMessages()
				}
				return nil
			case agentMessage := <-dc.inputChan: // receive messages
				if err := dc.processInputMessage(agentMessage); err != nil {
					dc.logger.Error(err)
				}
			case <-time.After(datachannelIdleTimeout):
				dc.logger.Info("Datachannel has been idle for too long, ceasing operation")
				return fmt.Errorf("cleaning up stale datachannel")
			}
		}
	})

	go dc.sendKeysplitting()
	go dc.zapPluginOutput()

	return dc, &dc.tmb, nil
}

func (d *DataChannel) handshakeOrTimeout() error {
	start := time.Now()
	select {
	case <-d.tmb.Dying():
		return nil
	case agentMessage := <-d.inputChan:
		// log the time it took to complete the handshake
		diff := time.Since(start)
		d.logger.Infof("It took %s to complete handshake", diff.Round(time.Millisecond).String())

		switch am.MessageType(agentMessage.MessageType) {
		case am.Error:
			return d.handleError(agentMessage)
		case am.Keysplitting:
			if err := d.handleKeysplitting(agentMessage); err != nil {
				return err
			} else {
				return nil
			}
		default:
			return fmt.Errorf("datachannel must start with a mrzap or error message, recieved: %s", agentMessage.MessageType)
		}
	case <-time.After(handshakeTimeout):
		return fmt.Errorf("handshake timed out")
	}
}

func (d *DataChannel) waitForRemainingMessages() error {
	checkOutboxInterval := time.Second

	for {
		select {
		// even if the plugin says it's done, we need to keep processing acks from the agent
		case agentMessage := <-d.inputChan:
			if err := d.processInputMessage(agentMessage); err != nil {
				d.logger.Error(err)
			}
		case <-time.After(checkOutboxInterval):
			// if the plugin has nothing pending and the pipeline is empty, we can safely stop
			if len(d.plugin.Outbox()) == 0 && d.keysplitter.IsPipelineEmpty() {
				return nil
			}
		}
	}
}

func (d *DataChannel) sendKeysplitting() error {
	for {
		select {
		case <-d.tmb.Dying():
			d.keysplitter.Release()
			return nil
		case ksMessage := <-d.keysplitter.Outbox():
			if ksMessage.Type == ksmsg.Syn || !d.keysplitter.Recovering() {
				d.send(am.Keysplitting, ksMessage)
			}
		}
	}
}

func (d *DataChannel) zapPluginOutput() error {
	for {
		select {
		case <-d.tmb.Dying():
			return nil
		case wrapper := <-d.plugin.Outbox():
			// Build and send response
			if err := d.keysplitter.Inbox(wrapper.Action, wrapper.ActionPayload); err != nil {
				d.logger.Errorf("could not build response message: %s", err)
			}
		}
	}
}

func (d *DataChannel) Close(reason error) {
	d.tmb.Kill(reason) // kills all datachannel, plugin, and action goroutines
}

func (d *DataChannel) openDataChannel(action string, synPayload interface{}) error {
	synMessage, err := d.keysplitter.BuildSyn(action, synPayload, false)
	if err != nil {
		return fmt.Errorf("error building syn: %w", err)
	}

	// Marshal the syn
	synBytes, err := json.Marshal(synMessage)
	if err != nil {
		return fmt.Errorf("error marshalling syn: %w", err)
	}

	messagePayload := OpenDataChannelPayload{
		Syn:    synBytes,
		Action: action,
	}

	// Marshal the messagePayload
	messagePayloadBytes, err := json.Marshal(messagePayload)
	if err != nil {
		return fmt.Errorf("error marshalling OpenDataChannelPayload: %w", err)
	}

	// send new datachannel message to agent, as we can build the syn here
	odMessage := am.AgentMessage{
		ChannelId:      d.id,
		MessagePayload: messagePayloadBytes,
		MessageType:    string(am.OpenDataChannel),
	}
	d.websocket.Send(odMessage)

	return nil
}

// Wraps and sends the payload
func (d *DataChannel) send(messageType am.MessageType, messagePayload interface{}) error {
	if messageBytes, err := json.Marshal(messagePayload); err != nil {
		return fmt.Errorf("failed to marshal the provided agent message payload: %s", messageBytes)
	} else {
		agentMessage := am.AgentMessage{
			ChannelId:      d.id,
			MessageType:    string(messageType),
			SchemaVersion:  am.CurrentVersion,
			MessagePayload: messageBytes,
		}

		// Push message to websocket channel output
		d.websocket.Send(agentMessage)
		return nil
	}
}

func (d *DataChannel) Receive(agentMessage am.AgentMessage) {
	if d.tmb.Alive() {
		d.inputChan <- &agentMessage
	}
}

func (d *DataChannel) processInputMessage(agentMessage *am.AgentMessage) error {
	d.logger.Debugf("Datachannel received %v message", agentMessage.MessageType)

	switch am.MessageType(agentMessage.MessageType) {
	case am.Error:
		if err := d.handleError(agentMessage); err != nil {
			// if we can't recover then shut everything down
			d.logger.Error(err)
			d.tmb.Kill(err)
		}
	case am.Keysplitting:
		if err := d.handleKeysplitting(agentMessage); err != nil {
			d.logger.Error(err)
		}
	case am.Stream:
		return d.handleStream(agentMessage)
	default:
		return fmt.Errorf("unhandled message type: %s", agentMessage.MessageType)
	}
	return nil
}

func (d *DataChannel) handleError(agentMessage *am.AgentMessage) error {
	var errMessage rrr.ErrorMessage
	if err := json.Unmarshal(agentMessage.MessagePayload, &errMessage); err != nil {
		return fmt.Errorf("could not unmarshal error message: %s", err)
	}

	if rrr.ErrorType(errMessage.Type) == rrr.KeysplittingValidationError {
		return d.keysplitter.Recover(errMessage)
	} else {
		return fmt.Errorf("received fatal %s error from agent: %s", errMessage.Type, errMessage.Message)
	}
}

func (d *DataChannel) handleStream(agentMessage *am.AgentMessage) error {
	var sMessage smsg.StreamMessage
	if err := json.Unmarshal(agentMessage.MessagePayload, &sMessage); err != nil {
		return fmt.Errorf("malformed Stream message")
	} else {
		d.plugin.ReceiveStream(sMessage)
		return nil
	}
}

func (d *DataChannel) handleKeysplitting(agentMessage *am.AgentMessage) error {
	// unmarshal the keysplitting message
	var ksMessage ksmsg.KeysplittingMessage
	if err := json.Unmarshal(agentMessage.MessagePayload, &ksMessage); err != nil {
		return fmt.Errorf("malformed Keysplitting message")
	}

	// validate keysplitting message
	if err := d.keysplitter.Validate(&ksMessage); err != nil {
		return fmt.Errorf("invalid keysplitting message: %s", err)
	}

	switch ksMessage.KeysplittingPayload.(type) {
	case ksmsg.SynAckPayload:
	case ksmsg.DataAckPayload:
		// Send message to plugin's input message handler
		if err := d.plugin.ReceiveKeysplitting(ksMessage.GetAction(), ksMessage.GetActionPayload()); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unhandled keysplitting type")
	}

	return nil
}
