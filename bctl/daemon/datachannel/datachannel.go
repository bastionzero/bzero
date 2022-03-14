package datachannel

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tomb "gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/db"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/web"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	rrr "bastionzero.com/bctl/v1/bzerolib/error"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
	bzdb "bastionzero.com/bctl/v1/bzerolib/plugin/db"
	bzkube "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	bzweb "bastionzero.com/bctl/v1/bzerolib/plugin/web"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	// max number of times we will try to resend after an error message
	maxRetries = 3
)

// Plugins this datachannel accepts
type PluginName string

const (
	Kube PluginName = "kube"
	Db   PluginName = "db"
	Web  PluginName = "web"
)

type OpenDataChannelPayload struct {
	Syn    []byte `json:"syn"`
	Action string `json:"action"`
}

type IKeysplitting interface {
	BuildSyn(action string, payload []byte) (ksmsg.KeysplittingMessage, error)
	Validate(ksMessage *ksmsg.KeysplittingMessage) error
	BuildResponse(ksMessage *ksmsg.KeysplittingMessage, action string, actionPayload []byte) (ksmsg.KeysplittingMessage, error)
}

type DataChannel struct {
	logger       *logger.Logger
	tmb          tomb.Tomb
	websocket    *websocket.Websocket
	id           string // DataChannel's ID
	ready        bool
	plugin       plugin.IPlugin
	keysplitting IKeysplitting
	handshook    bool // bool to indicate if we have received a valid syn ack (initally set to false)

	// channels for incoming messages
	inputChan chan am.AgentMessage

	// input channel for keysplitting messages only.  This allows for keysplitting messages to be
	// received in series without blocking stream messaages from being processed
	ksInputChan chan am.AgentMessage

	// If we need to send a SYN, then we need a way to keep
	// track of whatever message that triggered the send SYN
	onDeck      bzplugin.ActionWrapper
	lastMessage bzplugin.ActionWrapper
	retry       int

	// locks for our ondeck and lastmessage
	// TODO: Requiring these mutexes is probably indicative of a design/architecture failure
	onDeckLock      sync.Mutex
	lastMessageLock sync.Mutex
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
		handshook:    false,

		inputChan:   make(chan am.AgentMessage, 25),
		ksInputChan: make(chan am.AgentMessage, 25),

		onDeck: bzplugin.ActionWrapper{},
		retry:  0,
	}

	// register with websocket so datachannel can send a receive messages
	websocket.Subscribe(id, dc)

	// tell Bastion we're opening a datachannel and send SYN to agent initiates an authenticated datachannel
	if err := dc.openDataChannel(action, actionParams); err != nil {
		logger.Error(err)
		return nil, &tomb.Tomb{}, err
	}

	// start our plugin
	if err := dc.startPlugin(action, actionParams); err != nil {
		logger.Error(err)
		return nil, &tomb.Tomb{}, err
	}

	// listener for incoming messages
	dc.tmb.Go(func() error {
		defer websocket.Unsubscribe(id) // causes decoupling from websocket
		for {
			select {
			case <-parentTmb.Dying(): // daemon is dying
				return errors.New("daemon was orphaned too young and can't be batman :'(")
			case <-dc.tmb.Dying():
				dc.logger.Infof("Killing datachannel and its subsidiaries because %s", dc.tmb.Err())
				time.Sleep(30 * time.Second) // give datachannel time to close correctly
				return nil
			case agentMessage := <-dc.inputChan: // receive messages
				// Keysplitting needs to be its own routine because the function will get stuck waiting for user input after a DATA/ACK,
				// but still need to receive streams
				if err := dc.processInput(agentMessage); err != nil {
					dc.logger.Error(err)
				}
			}
		}
	})

	// listen and process keysplitting messages in series
	go func() {
		for {
			select {
			case <-dc.tmb.Dying():
				return
			case ksMessage := <-dc.ksInputChan:
				if err := dc.handleKeysplitting(ksMessage); err != nil {
					dc.logger.Error(err)
				}
			}
		}
	}()

	return dc, &dc.tmb, nil
}

func (d *DataChannel) Ready() bool {
	return d.ready
}

func (d *DataChannel) Close(reason error) {
	d.tmb.Kill(reason) // kills all datachannel, plugin, and action goroutines
	d.tmb.Wait()
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

func (d *DataChannel) startPlugin(action string, actionParams []byte) error {
	// parse our plugin name
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) == 0 {
		return fmt.Errorf("malformed action: %s", action)
	}
	pluginName := parsedAction[0]

	// start plugin based on name
	subLogger := d.logger.GetPluginLogger(pluginName)
	switch PluginName(pluginName) {
	case Kube:
		// Deserialize the action params
		var kubeParams bzkube.KubeActionParams
		if err := json.Unmarshal(actionParams, &kubeParams); err != nil {
			return fmt.Errorf("error deserializing actions params")
		}

		// start kube plugin
		if plugin, err := kube.New(&d.tmb, subLogger, kubeParams); err != nil {
			return fmt.Errorf("could not start kube daemon plugin: %s", err)
		} else {
			d.plugin = plugin
		}
	case Db:
		// Deserialize the action params
		var dbParams bzdb.DbActionParams
		if err := json.Unmarshal(actionParams, &dbParams); err != nil {
			return fmt.Errorf("error deserializing actions params")
		}

		// start db plugin
		if plugin, err := db.New(&d.tmb, subLogger, dbParams); err != nil {
			return fmt.Errorf("could not start db daemon plugin: %s", err)
		} else {
			d.plugin = plugin
		}
	case Web:
		// Deserialize the action params
		var webParams bzweb.WebActionParams
		if err := json.Unmarshal(actionParams, &webParams); err != nil {
			return fmt.Errorf("error deserializing actions params")
		}

		// start web plugin
		subLogger := d.logger.GetPluginLogger("WebDaemon")
		if plugin, err := web.New(&d.tmb, subLogger, webParams); err != nil {
			return fmt.Errorf("could not start db daemon plugin: %s", err)
		} else {
			d.plugin = plugin
		}
	default:
		return fmt.Errorf("unrecognized plugin passed to datachannel: %s", pluginName)
	}

	return nil
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

func (d *DataChannel) sendSyn(action string) error {
	d.logger.Info("Sending SYN")

	d.handshook = false
	payload := map[string]string{
		// "TargetUser":   d.targetUser,
		// "TargetGroups": strings.Join(d.targetGroups, ","),
	}
	payloadBytes, _ := json.Marshal(payload)

	if synMessage, err := d.keysplitting.BuildSyn(action, payloadBytes); err != nil {
		rerr := fmt.Errorf("error building Syn: %s", err)
		d.logger.Error(rerr)
		return rerr
	} else {
		d.send(am.Keysplitting, synMessage)
		return nil
	}
}

func (d *DataChannel) Receive(agentMessage am.AgentMessage) {
	d.inputChan <- agentMessage
}

func (d *DataChannel) processInput(agentMessage am.AgentMessage) error {
	d.logger.Infof("Datachannel received %v message", agentMessage.MessageType)

	var err error
	switch am.MessageType(agentMessage.MessageType) {
	case am.Keysplitting:
		d.ksInputChan <- agentMessage
	case am.Stream:
		err = d.handleStream(agentMessage)
	case am.Error:
		err = d.handleError(agentMessage)
	default:
		err = fmt.Errorf("unhandled Message type: %v", agentMessage.MessageType)
	}
	return err
}

func (d *DataChannel) handleError(agentMessage am.AgentMessage) error {
	var errMessage rrr.ErrorMessage
	if err := json.Unmarshal(agentMessage.MessagePayload, &errMessage); err != nil {
		return fmt.Errorf("malformed Error message")
	} else {
		rerr := fmt.Errorf("received error from agent: %s", errMessage.Message)

		// Keysplitting validation errors are probably going to be mostly bzcert renewals and
		// we don't want to break every time that happens so we need to get back on the ks train
		// executive decision: we don't retry if we get an error on a syn aka d.handshook == false
		if d.retry > maxRetries {
			rerr = fmt.Errorf("goodbye. retried too many times to fix error: %s", errMessage.Message)
			d.Close(rerr)
		} else if rrr.ErrorType(errMessage.Type) == rrr.KeysplittingValidationError && d.handshook {
			d.retry++
			d.setOnDeck(d.getLastMessage())

			// In order to get back on the keysplitting train, we need to resend the syn, get the synack
			// so that our input message handler is pointing to the right thing.
			if err := d.sendSyn(d.getOnDeck().Action); err != nil {
				return err
			}
		} else {
			d.Close(rerr)
		}

		return rerr
	}
}

func (d *DataChannel) handleStream(agentMessage am.AgentMessage) error {
	var sMessage smsg.StreamMessage
	if err := json.Unmarshal(agentMessage.MessagePayload, &sMessage); err != nil {
		return fmt.Errorf("malformed Stream message")
	}

	if d.plugin != nil {
		d.plugin.ReceiveStream(sMessage)
	} else {
		return fmt.Errorf("no active plugin")
	}
	return nil
}

// TODO: simplify this and have them both deserialize into a "common keysplitting" message
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

	switch ksMessage.Type {
	case ksmsg.SynAck:
		synAckPayload := ksMessage.KeysplittingPayload.(ksmsg.SynAckPayload)
		action = synAckPayload.Action
		actionResponsePayload = synAckPayload.ActionResponsePayload

		d.handshook = true
		d.ready = true

		// If there is a message that wasn't sent because we got a keysplitting validation error on it, send it now
		if next := d.getOnDeck(); next.Action != "" {
			return d.sendKeysplitting(&ksMessage, next.Action, next.ActionPayload)
		}
	case ksmsg.DataAck:
		// If we had something on deck, then this was the ack for it and we can remove it
		d.setOnDeck(bzplugin.ActionWrapper{})

		// If we're here, it means that the previous data message that caused the error was accepted
		d.retry = 0

		dataAckPayload := ksMessage.KeysplittingPayload.(ksmsg.DataAckPayload)
		action = dataAckPayload.Action
		actionResponsePayload = dataAckPayload.ActionResponsePayload
	default:
		return fmt.Errorf("unhandled Keysplitting type")
	}

	// Send message to plugin's input message handler
	if d.plugin != nil {
		if action, returnPayload, err := d.plugin.ReceiveKeysplitting(action, actionResponsePayload); err == nil {
			// sometimes when we kill the datachannel from the action, there is a final empty payload that sneaks out because
			// the process dies but that last messages are processed
			if action == "" {
				return nil
			}

			// We need to know the last message for invisible response to keysplitting validation errors
			d.setLastMessage(bzplugin.ActionWrapper{
				Action:        action,
				ActionPayload: returnPayload,
			})

			return d.sendKeysplitting(&ksMessage, action, returnPayload)

		} else {
			return err
		}
	} else {
		return fmt.Errorf("no plugin")
	}
}

func (d *DataChannel) sendKeysplitting(keysplittingMessage *ksmsg.KeysplittingMessage, action string, payload []byte) error {
	// Build and send response
	if respKSMessage, err := d.keysplitting.BuildResponse(keysplittingMessage, action, payload); err != nil {
		rerr := fmt.Errorf("could not build response message: %s", err)
		d.logger.Error(rerr)
		return rerr
	} else {
		d.send(am.Keysplitting, respKSMessage)
		return nil
	}
}

func (d *DataChannel) getOnDeck() bzplugin.ActionWrapper {
	d.onDeckLock.Lock()
	defer d.onDeckLock.Unlock()

	return d.onDeck
}

func (d *DataChannel) setOnDeck(msg bzplugin.ActionWrapper) {
	d.onDeckLock.Lock()
	defer d.onDeckLock.Unlock()

	d.onDeck = msg
}

func (d *DataChannel) getLastMessage() bzplugin.ActionWrapper {
	d.lastMessageLock.Lock()
	defer d.lastMessageLock.Unlock()

	return d.lastMessage
}

func (d *DataChannel) setLastMessage(msg bzplugin.ActionWrapper) {
	d.lastMessageLock.Lock()
	defer d.lastMessageLock.Unlock()

	d.lastMessage = msg
}
