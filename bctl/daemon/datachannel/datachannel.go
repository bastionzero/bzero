package datachannel

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tomb "gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/db"
	shell "bastionzero.com/bctl/v1/bctl/daemon/plugin/shell"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/web"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	rrr "bastionzero.com/bctl/v1/bzerolib/error"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	// max number of times we will try to resend after an error message
	maxErrorRecoveryTries = 3

	// amount of time we're willing to wait for our first keysplitting message
	handshakeTimeout = 20 * time.Second

	// maximum amount of time we want to keep this datachannel alive after
	// neither receiving nor sending anything
	datachannelLifetime = 8 * 24 * time.Hour
)

type OpenDataChannelPayload struct {
	Syn    []byte `json:"syn"`
	Action string `json:"action"`
}

type IKeysplitting interface {
	BuildSyn(action string, payload interface{}, send bool) (*ksmsg.KeysplittingMessage, error)
	Validate(ksMessage *ksmsg.KeysplittingMessage) error
	Recover(errorMessage rrr.ErrorMessage) error
	Recovering() bool
	Inbox(action string, actionPayload interface{}) error
	Outbox() <-chan *ksmsg.KeysplittingMessage
	Release()
}

type IPlugin interface {
	ReceiveKeysplitting(action string, actionPayload []byte) error
	ReceiveStream(smessage smsg.StreamMessage)
	Feed(food interface{}) error
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

	handshook bool

	// channels for incoming messages
	inputChan chan am.AgentMessage

	// if we receive an error than we ignore all messages in the mrzap queue until we hit a syn
	ignoreMrZAP bool

	errorRecoveryAttempt int
}

func New(logger *logger.Logger,
	id string,
	parentTmb *tomb.Tomb, // daemon has ability to rage quit and take everything down with it
	websocket websocket.IWebsocket,
	keysplitter IKeysplitting,
	action string,
	actionParams interface{},
	attach bool, // bool to indicate if we are attaching to an existing datachannel
) (*DataChannel, *tomb.Tomb, error) {

	dc := &DataChannel{
		websocket: websocket,
		logger:    logger,
		id:        id,

		keysplitter: keysplitter,
		handshook:   false,

		inputChan:   make(chan am.AgentMessage, 25),
		ignoreMrZAP: false,

		errorRecoveryAttempt: 0,
	}

	// register with websocket so datachannel can send and receive messages
	websocket.Subscribe(id, dc)

	// start our plugin
	if err := dc.startPlugin(action, actionParams, attach); err != nil {
		return nil, nil, err
	}

	if !attach {
		// tell Bastion we're opening a datachannel and send SYN to agent initiates an authenticated datachannel
		logger.Info("Sending request to agent to open a new datachannel")
		if err := dc.openDataChannel(action, actionParams); err != nil {
			return nil, nil, err
		}
	} else if _, err := keysplitter.BuildSyn(action, actionParams, true); err != nil {
		logger.Infof("Sending SYN on existing datachannel %s with actions %s.", dc.id, action)
	} else {
		return nil, nil, fmt.Errorf("failed to build and send syn for attachment flow")
	}

	dc.tmb.Go(func() error {
		defer websocket.Unsubscribe(id) // causes decoupling from websocket
		defer dc.logger.Infof("closing datachannel and its subsidiaries: %s", dc.tmb.Err())
		defer dc.logger.Info("DONE WITH MAIN GO ROUTINE")

		// dc.tmb.Go(dc.processIncomingKeysplitting)

		// wait for any message from the agent to come in
		if err := dc.handshakeOrTimeout(); err != nil {
			return err
		}

		for {
			select {
			case <-parentTmb.Dying(): // daemon is dying
				return fmt.Errorf("daemon was orphaned too young and can't be batman :'(")
			case <-dc.tmb.Dying():
				dc.plugin.Kill()
				return nil
			case <-dc.plugin.Done():

				// FIXME: this is my dumb way of waiting for all incoming messages to receive before dying
				// the purpose is more to absorb any errors than anything but we might be here a while
				// with this particular solution
				select {
				case <-dc.inputChan:
				case <-time.After(2 * time.Second):
					return fmt.Errorf("datachannel's sole plugin is closed")
				}
			case agentMessage := <-dc.inputChan: // receive messages
				if err := dc.processInput(agentMessage); err != nil {
					dc.logger.Error(err)
				}
			case <-time.After(datachannelLifetime):
				return fmt.Errorf("cleaning up stale datachannel")
			}
		}
	})

	// go dc.processIncomingKeysplitting()
	go dc.sendKeysplitting()
	go dc.zapPluginOutput()

	return dc, &dc.tmb, nil
}

func (d *DataChannel) handshakeOrTimeout() error {
	select {
	case <-d.tmb.Dying():
		return nil
	case agentMessage := <-d.inputChan:
		msgType := am.MessageType(agentMessage.MessageType)
		if msgType != am.Error && msgType != am.Keysplitting {
			return fmt.Errorf("invalid first message recieved, must be error or mrzap")
		} else if err := d.processInput(agentMessage); err != nil {
			return err
		} else {
			return nil
		}
	case <-time.After(handshakeTimeout):
		return fmt.Errorf("handshake timed out")
	}
}

// func (d *DataChannel) processIncomingKeysplitting() error {
// 	defer d.logger.Info("DONE WITH PROCESS INCOMING KEYSPLITTING")
// 	// wait initial syn/ack is received or timeout
// 	select {
// 	case <-d.tmb.Dying():
// 		return nil
// 	case ksMessage := <-d.ksInputChan:
// 		if err := d.handleKeysplitting(ksMessage); err != nil {
// 			d.logger.Error(err)
// 		}
// 	case <-time.After(handshakeTimeout):
// 		return fmt.Errorf("handshake timed out")
// 	}

// 	for {
// 		select {
// 		case <-d.tmb.Dying():
// 			return nil
// 		case ksMessage := <-d.ksInputChan:
// 			if err := d.handleKeysplitting(ksMessage); err != nil {
// 				d.logger.Error(err)
// 			}
// 		}
// 	}
// }

func (d *DataChannel) sendKeysplitting() error {
	defer d.logger.Info("DONE WITH SEND KEYSPLITTING")
	for {
		select {
		case <-d.tmb.Dying():
			d.keysplitter.Release()
			return nil
		case ksMessage := <-d.keysplitter.Outbox():
			// d.logger.Infof("WE'RE SENDING SOME OUTPUT")
			if ksMessage.Type == ksmsg.Syn {
				d.ignoreMrZAP = false
			}
			if !d.ignoreMrZAP {
				d.send(am.Keysplitting, ksMessage)
			}
		}
		// time.Sleep(1 * time.Second)
	}
}

func (d *DataChannel) zapPluginOutput() error {
	defer d.logger.Info("DONE WITH ZAP PLUGIN OUTPUT")
	for {
		select {
		case <-d.tmb.Dying():
			return nil
		case wrapper := <-d.plugin.Outbox():
			// d.logger.Infof("PLUGIN OUTPUT HEADED FOR KEYSPLITTING")
			// Build and send response
			if err := d.keysplitter.Inbox(wrapper.Action, wrapper.ActionPayload); err != nil {
				d.logger.Errorf("could not build response message: %s", err)
			}
		}
	}
}

func (d *DataChannel) Ready() bool {
	return d.handshook
}

func (d *DataChannel) Close(reason error) {
	d.logger.Infof("killing datachannel tomb")
	d.tmb.Kill(reason) // kills all datachannel, plugin, and action goroutines
}

func (d *DataChannel) openDataChannel(action string, actionParams interface{}) error {
	synMessage, err := d.keysplitter.BuildSyn(action, actionParams, false)
	if err != nil {
		return fmt.Errorf("error building syn: %s", err)
	}

	// Marshal the syn
	synBytes, err := json.Marshal(synMessage)
	if err != nil {
		return fmt.Errorf("error marshalling syn: %s", err)
	}

	messagePayload := OpenDataChannelPayload{
		Syn:    synBytes,
		Action: action,
	}

	// Marshall the messagePayload
	messagePayloadBytes, err := json.Marshal(messagePayload)
	if err != nil {
		return fmt.Errorf("error marshalling OpenDataChannelPayload: %s", err)
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

func (d *DataChannel) startPlugin(action string, actionParams interface{}, attach bool) error {
	// start plugin based on name
	pluginName := parsePluginName(action)
	subLogger := d.logger.GetPluginLogger(string(pluginName))

	var err error
	switch pluginName {
	// case Kube:
	// 	if d.plugin, err = kube.New(subLogger, kubeParams); err != nil {
	// 		return fmt.Errorf("could not start kube daemon plugin: %s", err)
	// 	}
	case bzplugin.Db:
		if d.plugin, err = db.New(subLogger, actionParams); err != nil {
			return fmt.Errorf("could not start db daemon plugin: %s", err)
		}
	case bzplugin.Web:
		if d.plugin, err = web.New(subLogger, actionParams); err != nil {
			return fmt.Errorf("could not start web daemon plugin: %s", err)
		}
	case bzplugin.Shell:
		if d.plugin, err = shell.New(subLogger, actionParams, attach); err != nil {
			return fmt.Errorf("could not start shell daemon plugin: %s", err)
		}
	default:
		return fmt.Errorf("unrecognized plugin: %s", pluginName)
	}

	d.logger.Infof("Started %v plugin", pluginName)
	return nil
}

func parsePluginName(action string) bzplugin.PluginName {
	// parse our plugin name
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) == 0 {
		return ""
	} else {
		return bzplugin.PluginName(parsedAction[0])
	}
}

func (d *DataChannel) Feed(data interface{}) error {
	d.logger.Info("TRYING TO EAT SOMETHING")
	if d.tmb.Err() != tomb.ErrStillAlive {
		return fmt.Errorf("could not feed plugin because datachannel is dead")
	}

	if d.plugin != nil {
		return d.plugin.Feed(data)
	} else {
		return fmt.Errorf("no plugin is associated with this datachannel")
	}
}

// Wraps and sends the payload
func (d *DataChannel) send(messageType am.MessageType, messagePayload interface{}) error {
	messageBytes, _ := json.Marshal(messagePayload)
	agentMessage := am.AgentMessage{
		ChannelId:      d.id,
		MessageType:    string(messageType),
		SchemaVersion:  am.SchemaVersion,
		MessagePayload: messageBytes,
	}

	// if the datachannel isn't ready, wait until it is
	if !d.handshook {
		d.logger.Info("Datachannel not ready yet, waiting until it is")

		// starts a ticket to check if the datachannel is ready
		readyCheckTicker := time.NewTicker(10 * time.Millisecond)
		notificationTicker := time.NewTicker(5 * time.Second)
	CheckReadyLoop:
		for {
			select {
			case <-d.tmb.Dying():
				return nil
			case <-readyCheckTicker.C:
				if d.handshook {
					d.logger.Info("DataChannel ready, sending skittles")
					break CheckReadyLoop
				}
			case <-notificationTicker.C:
				d.logger.Info("Still waiting...")
			}
		}
	}

	// Push message to websocket channel output
	d.websocket.Send(agentMessage)
	return nil
}

func (d *DataChannel) Receive(agentMessage am.AgentMessage) {
	if d.tmb.Err() == tomb.ErrStillAlive {
		d.inputChan <- agentMessage
	}
}

func (d *DataChannel) processInput(agentMessage am.AgentMessage) error {
	d.logger.Infof("Datachannel received %v message", agentMessage.MessageType)

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

func (d *DataChannel) handleError(agentMessage am.AgentMessage) error {
	var errMessage rrr.ErrorMessage
	if err := json.Unmarshal(agentMessage.MessagePayload, &errMessage); err != nil {
		return fmt.Errorf("could not unmarshal error message: %s", err)
	}

	switch {
	case d.keysplitter.Recovering():
		d.logger.Debugf("ignoring error message because we're already in recovery")
		return nil
	case d.errorRecoveryAttempt >= maxErrorRecoveryTries:
		return fmt.Errorf("retried too many times to fix error: %s", errMessage.Message)
	case !d.handshook:
		return fmt.Errorf("daemon cannot recover from an error on the initial syn")
	case rrr.ErrorType(errMessage.Type) == rrr.KeysplittingValidationError:
		// stop processing keysplitting messages until we start recovery with a syn
		d.ignoreMrZAP = true

		if err := d.keysplitter.Recover(errMessage); err != nil {
			d.logger.Debugf("ignoring error because %s", err)
			return nil
		} else {
			d.errorRecoveryAttempt++
			d.logger.Infof("Attempting to recover from error, attempt #%d", d.errorRecoveryAttempt)
			return nil
		}
	default:
		return fmt.Errorf("received fatal error from agent: %s", errMessage.Message)
	}
}

func (d *DataChannel) handleStream(agentMessage am.AgentMessage) error {
	var sMessage smsg.StreamMessage
	if err := json.Unmarshal(agentMessage.MessagePayload, &sMessage); err != nil {
		return fmt.Errorf("malformed Stream message")
	} else if d.plugin == nil {
		return fmt.Errorf("no active plugin")
	} else {
		d.plugin.ReceiveStream(sMessage)
		return nil
	}
}

func (d *DataChannel) handleKeysplitting(agentMessage am.AgentMessage) error {
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
		d.handshook = true
	case ksmsg.DataAckPayload:
		// If we're here, it means that the previous data message that caused the error was accepted
		d.errorRecoveryAttempt = 0

		// Send message to plugin's input message handler
		if d.plugin != nil {
			if err := d.plugin.ReceiveKeysplitting(ksMessage.GetAction(), ksMessage.GetActionPayload()); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("cannot send message to non-existent plugin")
		}
	default:
		return fmt.Errorf("unhandled keysplitting type")
	}

	return nil
}
