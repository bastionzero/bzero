package datachannel

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tomb "gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/db"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/web"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	rrr "bastionzero.com/bctl/v1/bzerolib/error"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzdb "bastionzero.com/bctl/v1/bzerolib/plugin/db"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	// max number of times we will try to resend after an error message
	maxErrorRecoveryTries = 3
)

type OpenDataChannelPayload struct {
	Syn    []byte `json:"syn"`
	Action string `json:"action"`
}

type IKeysplitting interface {
	BuildSyn(action string, payload []byte) (ksmsg.KeysplittingMessage, error)
	Validate(ksMessage *ksmsg.KeysplittingMessage) error
	Recover(errorMessage rrr.ErrorMessage) error
	// BuildResponse(ksMessage *ksmsg.KeysplittingMessage, action string, actionPayload []byte) (ksmsg.KeysplittingMessage, error)
	Inbox(action string, actionPayload []byte) error
	Outbox() <-chan *ksmsg.KeysplittingMessage
}

type IPlugin interface {
	ReceiveKeysplitting(action string, actionPayload []byte) error
	ReceiveStream(smessage smsg.StreamMessage)
	Feed(food interface{}) error
	Outbox() <-chan plugin.ActionWrapper
	Done() <-chan struct{}
	// Stop()
}

type DataChannel struct {
	logger       *logger.Logger
	tmb          tomb.Tomb
	websocket    *websocket.Websocket
	id           string // DataChannel's ID
	ready        bool
	plugin       IPlugin
	keysplitting IKeysplitting
	attach       bool // bool to indicate if we are attaching to an existing datachannel
	// handshook    bool // bool to indicate if we have received a valid syn ack (initally set to false)

	// channels for incoming messages
	inputChan chan am.AgentMessage

	// input channel for keysplitting messages only.  This allows for keysplitting messages to be
	// received in series without blocking stream messaages from being processed
	ksInputChan chan am.AgentMessage

	// if we receive an error than we ignore all messages in the mrzap queue until we hit a syn
	sendks bool

	// If we need to send a SYN, then we need a way to keep
	// track of whatever message that triggered the send SYN
	// onDeck      bzplugin.ActionWrapper
	// lastMessage bzplugin.ActionWrapper
	errorRecoveryAttempt int

	// locks for our ondeck and lastmessage
	// TODO: Requiring these mutexes is probably indicative of a design/architecture failure
	// onDeckLock      sync.Mutex
	// lastMessageLock sync.Mutex
}

func New(logger *logger.Logger,
	id string,
	parentTmb *tomb.Tomb, // daemon has ability to rage quit and take everything down with it
	websocket *websocket.Websocket,
	refreshTokenCommand string,
	configPath string,
	action string,
	actionParams []byte,
	agentPubKey string,
	attach bool,
) (*DataChannel, *tomb.Tomb, error) {

	keysplitter, err := keysplitting.New(agentPubKey, configPath, refreshTokenCommand)
	if err != nil {
		logger.Error(err)
		return nil, &tomb.Tomb{}, err
	}

	dc := &DataChannel{
		websocket: websocket,
		logger:    logger,
		id:        id,
		ready:     false,

		keysplitting: keysplitter,
		attach:       attach,
		// handshook:    false,

		inputChan:   make(chan am.AgentMessage, 25),
		ksInputChan: make(chan am.AgentMessage, 25),
		sendks:      false,

		// onDeck:      bzplugin.ActionWrapper{},
		// lastMessage: bzplugin.ActionWrapper{},
		// retry:       0,
	}

	// register with websocket so datachannel can send a receive messages
	websocket.Subscribe(id, dc)

	if !attach {
		// tell Bastion we're opening a datachannel and send SYN to agent initiates an authenticated datachannel
		logger.Info("Sending request to agent to open a new datachannel")
		if err := dc.openDataChannel(action, actionParams); err != nil {
			logger.Error(err)
			return nil, &tomb.Tomb{}, err
		}
	} else {
		logger.Infof("Sending SYN on existing datachannel %s with actions %s.", dc.id, action)
		dc.sendSyn(action)
	}

	pluginName, err := getPluginNameFromAction(action)
	if err != nil {
		return nil, &tomb.Tomb{}, err
	}

	// start our plugin
	if err := dc.startPlugin(pluginName, actionParams); err != nil {
		logger.Error(err)
		return nil, &tomb.Tomb{}, err
	}

	dc.tmb.Go(func() error {
		defer websocket.Unsubscribe(id) // causes decoupling from websocket

		dc.tmb.Go(dc.incomingKeysplittingProcessor)
		dc.tmb.Go(dc.outgoingKeysplittingSender)
		dc.tmb.Go(dc.outgoingPluginMessageProcessor)

		for {
			select {
			case <-parentTmb.Dying(): // daemon is dying
				return errors.New("daemon was orphaned too young and can't be batman :'(")
			case <-dc.tmb.Dying():
				dc.logger.Infof("Killing datachannel and its subsidiaries because %s", dc.tmb.Err())
				// Don't sleep in the case of the shell plugin because it will
				// cause the daemon to not exit right away and the zli connect
				// command will hang. This shouldnt be an issue for shell
				// because each shell connection starts a separate daemon
				// process and the datachannel will be killed on the agent side
				// when the daemon websocket disconnects
				if pluginName != bzplugin.Shell {
					time.Sleep(10 * time.Second) // give datachannel time to close correctly
				}
				return nil
			case <-dc.plugin.Done():
				dc.Close(fmt.Errorf("datachannel's sole action is closed"))
			case agentMessage := <-dc.inputChan: // receive messages
				if err := dc.processInput(agentMessage); err != nil {
					dc.logger.Error(err)
				}
			}
		}
	})

	return dc, &dc.tmb, nil
}

func (d *DataChannel) incomingKeysplittingProcessor() error {
	for {
		select {
		case <-d.tmb.Dying():
			return nil
		case ksMessage := <-d.ksInputChan:
			if err := d.handleKeysplitting(ksMessage); err != nil {
				d.logger.Error(err)
			}
		}
	}
}

func (d *DataChannel) outgoingKeysplittingSender() error {
	for {
		select {
		case <-d.tmb.Dying():
			return nil
		case ksMessage := <-d.keysplitting.Outbox():
			if ksMessage.Type == ksmsg.Syn {
				d.sendks = true
			}

			if d.sendks {
				d.send(am.Keysplitting, ksMessage)
			}
		}
	}
}

func (d *DataChannel) outgoingPluginMessageProcessor() error {
	for {
		select {
		case <-d.tmb.Dying():
			return nil
		case wrapper := <-d.plugin.Outbox():
			// Build and send response
			if err := d.keysplitting.Inbox(wrapper.Action, wrapper.ActionPayload); err != nil {
				d.logger.Errorf("could not build response message: %s", err)
			}
		}
	}
}

func (d *DataChannel) Ready() bool {
	return d.ready
}

func (d *DataChannel) Close(reason error) {
	d.logger.Infof("killing datachannel tomb")
	d.tmb.Kill(reason) // kills all datachannel, plugin, and action goroutines
}

func (d *DataChannel) openDataChannel(action string, actionParams []byte) error {
	synMessage, err := d.keysplitting.BuildSyn(action, actionParams)
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

func (d *DataChannel) startPlugin(pluginName bzplugin.PluginName, actionParams []byte) error {
	// start plugin based on name
	pluginName := parsePluginName(action)
	subLogger := d.logger.GetPluginLogger(string(pluginName))
	switch pluginName {
	// case Kube:
	// 	// Deserialize the action params
	// 	var kubeParams bzkube.KubeActionParams
	// 	if err := json.Unmarshal(actionParams, &kubeParams); err != nil {
	// 		return fmt.Errorf("error deserializing actions params")
	// 	} else if d.plugin, err = kube.New(&d.tmb, subLogger, kubeParams); err != nil {
	// 		return fmt.Errorf("could not start kube daemon plugin: %s", err)
	// 	}
	case bzplugin.Db:
		// Deserialize the action params
		var dbParams bzdb.DbActionParams
		if err := json.Unmarshal(actionParams, &dbParams); err != nil {
			return fmt.Errorf("error deserializing actions params")
		} else if d.plugin, err = db.New(&d.tmb, subLogger, dbParams); err != nil {
			return fmt.Errorf("could not start db daemon plugin: %s", err)
		}
	case bzplugin.Kube:
		// Deserialize the action params
		var kubeParams bzkube.KubeActionParams
		if err := json.Unmarshal(actionParams, &kubeParams); err != nil {
			return fmt.Errorf("error deserializing actions params")
		} else if d.plugin, err = web.New(&d.tmb, subLogger, webParams); err != nil {
			return fmt.Errorf("could not start web daemon plugin: %s", err)
		}
	// case bzplugin.Shell:
	// 	// Deserialize the action params
	// 	var shellParams bzshell.ShellActionParams
	// 	if err := json.Unmarshal(actionParams, &shellParams); err != nil {
	// 		return fmt.Errorf("error deserializing actions params")
	// 	}

	// 	// start shell plugin
	// 	if plugin, err := shell.New(&d.tmb, subLogger, shellParams, d.attach); err != nil {
	// 		return fmt.Errorf("could not start shell daemon plugin: %s", err)
	// 	} else {
	// 		d.plugin = plugin
	// 	}
	default:
		return fmt.Errorf("unrecognized plugin passed to datachannel: %s", pluginName)
	}

	return nil
}

func parsePluginName(action string) PluginName {
	// parse our plugin name
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) == 0 {
		return ""
	}
	return PluginName(parsedAction[0])
}

func (d *DataChannel) Feed(data interface{}) error {
	if d.plugin != nil {
		d.plugin.Feed(data)
		return nil
	} else {
		rerr := fmt.Errorf("no plugin is associated with this datachannel")
		d.logger.Error(rerr)
		return rerr
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
	if !d.ready {
		d.logger.Info("Datachannel not ready yet, waiting until it is")

		// starts a ticket to check if the datachannel is ready
		ticker := time.NewTicker(10 * time.Millisecond)
		for {
			<-ticker.C
			if d.ready {
				d.logger.Info("DataChannel ready, sending skittles")
				break
			}
		}
	}

	// Push message to websocket channel output
	d.websocket.Send(agentMessage)
	return nil
}

func getPluginNameFromAction(action string) (bzplugin.PluginName, error) {
	// parse our plugin name
	parsedAction := strings.Split(action, "/")

	if len(parsedAction) == 0 {
		return "", fmt.Errorf("malformed action: %s", action)
	}
	pluginName := parsedAction[0]

	return bzplugin.PluginName(pluginName), nil
}

// func (d *DataChannel) sendSyn(action string) error {
// 	d.logger.Info("Sending SYN")

// 	// d.handshook = false

// 	if synMessage, err := d.keysplitting.BuildSyn(action, []byte{}); err != nil {
// 		rerr := fmt.Errorf("error building Syn: %s", err)
// 		d.logger.Error(rerr)
// 		return rerr
// 	} else {
// 		d.send(am.Keysplitting, synMessage)
// 		return nil
// 	}
// }

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
			d.Close(err)
		}
	case am.Keysplitting:
		d.ksInputChan <- agentMessage
	case am.Stream:
		return d.handleStream(agentMessage)
	default:
		return fmt.Errorf("unhandled Message type: %v", agentMessage.MessageType)
	}
	return nil
}

func (d *DataChannel) handleError(agentMessage am.AgentMessage) error {
	var errMessage rrr.ErrorMessage
	if err := json.Unmarshal(agentMessage.MessagePayload, &errMessage); err != nil {
		return fmt.Errorf("malformed Error message")
	}

	if rrr.ErrorType(errMessage.Type) == rrr.KeysplittingValidationError {
		if !d.sendks {
			// if we're already recovering, then ignore
			return nil
		}
		// stop processing keysplitting messages until we start recovery (with a syn)
		d.sendks = false

		if d.errorRecoveryAttempt < maxErrorRecoveryTries {
			return fmt.Errorf("goodbye. retried too many times to fix error: %s", errMessage.Message)
		} else if err := d.keysplitting.Recover(errMessage); err != nil {
			return err
		} else {
			d.errorRecoveryAttempt++
			d.logger.Infof("Attempting to recover from error, attempt #%d", d.errorRecoveryAttempt)
			return nil
		}
	} else {
		return fmt.Errorf("received fatal error from agent: %s", errMessage.Message)
	}
}

// 	var errMessage rrr.ErrorMessage
// 	if err := json.Unmarshal(agentMessage.MessagePayload, &errMessage); err != nil {
// 		return fmt.Errorf("malformed Error message")
// 	} else {
// 		rerr := fmt.Errorf("received error from agent: %s", errMessage.Message)

// 		// Keysplitting validation errors are probably going to be mostly bzcert renewals and
// 		// we don't want to break every time that happens so we need to get back on the ks train
// 		// executive decision: we don't retry if we get an error on a syn aka d.handshook == false
// 		if d.retry > maxRetries {
// 			rerr = fmt.Errorf("goodbye. retried too many times to fix error: %s", errMessage.Message)
// 			d.Close(rerr)
// 		} else if rrr.ErrorType(errMessage.Type) == rrr.KeysplittingValidationError && d.handshook {
// 			d.retry++
// 			d.setOnDeck(d.getLastMessage())

// 			// In order to get back on the keysplitting train, we need to resend the syn, get the synack
// 			// so that our input message handler is pointing to the right thing.
// 			if err := d.sendSyn(d.getOnDeck().Action); err != nil {
// 				return err
// 			}
// 		} else {
// 			d.logger.Errorf("Please login again, %s", rerr)
// 			d.Close(rerr)
// 		}

// 		return rerr
// 	}
// }

func (d *DataChannel) handleStream(agentMessage am.AgentMessage) error {
	var sMessage smsg.StreamMessage
	if err := json.Unmarshal(agentMessage.MessagePayload, &sMessage); err != nil {
		return fmt.Errorf("malformed Stream message")
	}

	if d.plugin != nil {
		d.plugin.ReceiveStream(sMessage)
		return nil
	} else {
		return fmt.Errorf("no active plugin")
	}
}

func (d *DataChannel) handleKeysplitting(agentMessage am.AgentMessage) error {
	// unmarshal the keysplitting message
	var ksMessage ksmsg.KeysplittingMessage
	if err := json.Unmarshal(agentMessage.MessagePayload, &ksMessage); err != nil {
		return fmt.Errorf("malformed Keysplitting message")
	}

	// validate keysplitting message
	if err := d.keysplitting.Validate(&ksMessage); err != nil {
		return fmt.Errorf("invalid keysplitting message: %s", err)
	}

	// processInput message
	var action string
	var actionResponsePayload []byte

	// TODO: could these types be abstracted into an ack interface so we can handle them both with the same code?
	switch msg := ksMessage.KeysplittingPayload.(type) {
	case ksmsg.SynAckPayload:
		action = msg.Action
		actionResponsePayload = msg.ActionResponsePayload

		// d.handshook = true
		d.ready = true

		// If there is a message that wasn't sent because we got a keysplitting validation error on it, send it now
		// if next := d.getOnDeck(); next.Action != "" {
		// 	return d.sendKeysplitting(&ksMessage, next.Action, next.ActionPayload)
		// }
	case ksmsg.DataAckPayload:
		action = msg.Action
		actionResponsePayload = msg.ActionResponsePayload

		// If the syn is not what was causing the error message than this way we'll slowly inch our way towards the
		// offender and eventually reach our maxerrorretry limit because the offending message will never be ack'ed
		d.errorRecoveryAttempt = 0

		// If we had something on deck, then this was the ack for it and we can remove it
		// d.setOnDeck(bzplugin.ActionWrapper{})

		// If we're here, it means that the previous data message that caused the error was accepted
		// d.retry = 0
	default:
		return fmt.Errorf("unhandled Keysplitting type")
	}

	// Send message to plugin's input message handler
	if d.plugin != nil {
		if err := d.plugin.ReceiveKeysplitting(action, actionResponsePayload); err != nil {
			return err
		}

		// sometimes when we kill the datachannel from the action, there is a final empty payload that sneaks out because
		// the process dies but that last messages are processed
		// if action == "" {
		// 	return nil
		// }

		// We need to know the last message for invisible response to keysplitting validation errors
		// d.setLastMessage(bzplugin.ActionWrapper{
		// 	Action:        action,
		// 	ActionPayload: returnPayload,
		// })

		// return d.sendKeysplitting(&ksMessage, action, returnPayload)

		// } else {
		// 	return err
		// }
	} else {
		return fmt.Errorf("cannot send message to non-existent plugin")
	}
	return nil
}

// func (d *DataChannel) sendKeysplitting(keysplittingMessage *ksmsg.KeysplittingMessage, action string, payload []byte) error {

// 	// Build and send response
// 	if respKSMessage, err := d.keysplitting.BuildResponse(keysplittingMessage, action, payload); err != nil {
// 		rerr := fmt.Errorf("could not build response message: %s", err)
// 		d.logger.Error(rerr)
// 		return rerr
// 	} else {
// 		d.send(am.Keysplitting, respKSMessage)
// 		return nil
// 	}
// }

// func (d *DataChannel) getOnDeck() bzplugin.ActionWrapper {
// 	d.onDeckLock.Lock()
// 	defer d.onDeckLock.Unlock()

// 	return d.onDeck
// }

// func (d *DataChannel) setOnDeck(msg bzplugin.ActionWrapper) {
// 	d.onDeckLock.Lock()
// 	defer d.onDeckLock.Unlock()

// 	d.onDeck = msg
// }

// func (d *DataChannel) getLastMessage() bzplugin.ActionWrapper {
// 	d.lastMessageLock.Lock()
// 	defer d.lastMessageLock.Unlock()

// 	return d.lastMessage
// }

// func (d *DataChannel) setLastMessage(msg bzplugin.ActionWrapper) {
// 	d.lastMessageLock.Lock()
// 	defer d.lastMessageLock.Unlock()

// 	d.lastMessage = msg
// }
