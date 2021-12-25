package datachannel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	tomb "gopkg.in/tomb.v2"

	agms "bastionzero.com/bctl/v1/bctl/agent/plugin/kube"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	rrr "bastionzero.com/bctl/v1/bzerolib/error"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	// max number of times we will try to resend after an error message
	maxRetries = 3
)

type OpenDataChannelPayload struct {
	Syn    []byte `json:"syn"`
	Action string `json:"action"`
}

type DataChannel struct {
	websocket *websocket.Websocket
	logger    *logger.Logger
	id        string // DataChannel's ID
	tmb       tomb.Tomb
	ready     bool

	plugin       *kube.KubeDaemonPlugin
	keysplitting keysplitting.IKeysplitting
	handshook    bool // aka whether we need to send a syn

	// channels for incoming and outgoing messages
	inputChan chan am.AgentMessage

	// input channel for keysplitting messages only.  This allows for keysplitting messages to be
	// received in series without blocking stream messaages from being processed
	ksInputChan chan am.AgentMessage

	// If we need to send a SYN, then we need a way to keep
	// track of whatever message that triggered the send SYN
	onDeck      plugin.ActionWrapper
	lastMessage plugin.ActionWrapper
	retry       int
}

func New(logger *logger.Logger,
	id string,
	parentTmb *tomb.Tomb, // daemon has ability to rage quit and take everything down with it
	websocket *websocket.Websocket,
	refreshTokenCommand string,
	configPath string,
	action string,
	actionParams []byte) (*DataChannel, *tomb.Tomb, error) {

	keysplitter, err := keysplitting.New("", configPath, refreshTokenCommand)
	if err != nil {
		logger.Error(err)
		return &DataChannel{}, &tomb.Tomb{}, err
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

		onDeck: plugin.ActionWrapper{},
		retry:  0,
	}

	// register with websocket so datachannel can send a receive messages
	websocket.Subscribe(id, dc)

	// listener for incoming messages
	dc.tmb.Go(func() error {
		defer websocket.Unsubscribe(id) // causes decoupling from websocket
		for {
			select {
			case <-parentTmb.Dying(): // daemon is dying
				return errors.New("daemon was orphaned too young and can't be batman :'(")
			case <-dc.tmb.Dying():
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

	// start plugin based on action
	if strings.HasPrefix(action, "kube") {
		if err := dc.startKubeDaemonPlugin(action, actionParams); err != nil {
			logger.Error(err)
			return &DataChannel{}, &dc.tmb, err
		}
	} else {
		err := fmt.Errorf("unhandled action passed to daemon datachannel: %s", action)
		logger.Error(err)
		return &DataChannel{}, &tomb.Tomb{}, err
	}

	return dc, &dc.tmb, nil
}

func (d *DataChannel) Ready() bool {
	return d.ready
}

func (d *DataChannel) Close(reason error) {
	d.tmb.Kill(reason) // kills all datachannel, plugin, and action goroutines
	d.tmb.Wait()
}

func (d *DataChannel) startKubeDaemonPlugin(action string, actionParams []byte) error {
	// Deserialize the action params
	var actionParamsDeserialized agms.KubeActionParams
	if err := json.Unmarshal(actionParams, &actionParamsDeserialized); err != nil {
		rerr := fmt.Errorf("error deserializing actions params")
		d.logger.Error(rerr)
		return rerr
	}

	synMessage, buildSynErr := d.keysplitting.BuildSyn(action, actionParams)
	if buildSynErr != nil {
		d.logger.Errorf("error building syn")
		return buildSynErr
	}

	// Marshal the syn
	synBytes, synMarshalErr := json.Marshal(synMessage)
	if synMarshalErr != nil {
		d.logger.Errorf("error marshalling syn")
		return synMarshalErr
	}

	messagePayload := OpenDataChannelPayload{
		Syn:    synBytes,
		Action: action,
	}

	// Marshall the messagePayload
	messagePayloadBytes, marshalErr := json.Marshal(messagePayload)
	if marshalErr != nil {
		d.logger.Errorf("error marshalling OpenDataChannelPayload")
		return marshalErr
	}

	// send new datachannel message to agent, as we can build the syn here
	odMessage := am.AgentMessage{
		ChannelId:      d.id,
		MessagePayload: messagePayloadBytes,
		MessageType:    string(am.OpenDataChannel),
	}
	d.websocket.Send(odMessage)

	subLogger := d.logger.GetPluginLogger("KubeDaemon")
	if plugin, err := kube.New(&d.tmb, subLogger, actionParamsDeserialized); err != nil {
		rerr := fmt.Errorf("could not start kube daemon plugin: %s", err)
		d.logger.Error(rerr)
		return rerr
	} else {
		d.plugin = plugin
	}

	// go func() {
	// 	d.sendSyn(action)
	// }()

	return nil
}

func (d *DataChannel) Feed(action string, logId string, command string, w http.ResponseWriter, r *http.Request) error {
	if d.plugin != nil {
		d.plugin.Feed(action, logId, command, w, r)
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
	case am.DataChannelReady:
		d.ready = true
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
		rerr := fmt.Errorf("malformed Error message")
		d.logger.Error(rerr)
		return rerr
	} else {
		rerr := fmt.Errorf("received error from agent: %s", errMessage.Message)
		d.logger.Error(rerr)

		// Keysplitting validation errors are probably going to be mostly bzcert renewals and
		// we don't want to break every time that happens so we need to get back on the ks train
		// executive decision: we don't retry if we get an error on a syn aka d.handshook == false
		if d.retry > maxRetries {
			rerr = fmt.Errorf("retried too many times to fix error: %s", errMessage.Message)
		} else if rrr.ErrorType(errMessage.Type) == rrr.KeysplittingValidationError && d.handshook {
			d.retry++
			d.onDeck = d.lastMessage

			// In order to get back on the keysplitting train, we need to resend the syn, get the synack
			// so that our input message handler is pointing to the right thing.
			if err := d.sendSyn(d.onDeck.Action); err != nil {
				d.logger.Error(err)
				return err
			} else {
				return rerr
			}
		}

		d.tmb.Kill(rerr)
		return rerr
	}
}

func (d *DataChannel) handleStream(agentMessage am.AgentMessage) error {
	var sMessage smsg.StreamMessage
	if err := json.Unmarshal(agentMessage.MessagePayload, &sMessage); err != nil {
		rerr := fmt.Errorf("malformed Stream message")
		d.logger.Error(rerr)
		return rerr
	}

	d.plugin.ReceiveStream(sMessage)
	return nil
}

// TODO: simplify this and have them both deserialize into a "common keysplitting" message
func (d *DataChannel) handleKeysplitting(agentMessage am.AgentMessage) error {
	// unmarshal the keysplitting message
	var ksMessage ksmsg.KeysplittingMessage
	if err := json.Unmarshal(agentMessage.MessagePayload, &ksMessage); err != nil {
		rerr := fmt.Errorf("malformed Keysplitting message")
		d.logger.Error(rerr)
		return rerr
	}

	// validate keysplitting message
	if err := d.keysplitting.Validate(&ksMessage); err != nil {
		rerr := fmt.Errorf("invalid keysplitting message: %s", err)
		d.logger.Error(rerr)
		return rerr
	}

	// processInput message
	var action string
	var actionResponsePayload []byte
	d.logger.Infof("%v", ksMessage.Type)
	switch ksMessage.Type {
	case ksmsg.SynAck:
		synAckPayload := ksMessage.KeysplittingPayload.(ksmsg.SynAckPayload)
		action = synAckPayload.Action
		actionResponsePayload = synAckPayload.ActionResponsePayload

		d.handshook = true

		// If there is a message that wasn't sent because we got a keysplitting validation error on it, send it now
		if d.onDeck.Action != "" {
			err := d.sendKeysplitting(&ksMessage, d.onDeck.Action, d.onDeck.ActionPayload)
			return err
		}
	case ksmsg.DataAck:
		// If we had something on deck, then this was the ack for it and we can remove it
		d.onDeck = plugin.ActionWrapper{}

		// If we're here, it means that the previous data message that caused the error was accepted
		d.retry = 0

		dataAckPayload := ksMessage.KeysplittingPayload.(ksmsg.DataAckPayload)
		action = dataAckPayload.Action
		actionResponsePayload = dataAckPayload.ActionResponsePayload
	default:
		rerr := fmt.Errorf("unhandled Keysplitting type")
		d.logger.Error(rerr)
		return rerr
	}

	// Send message to plugin's input message handler
	if action, returnPayload, err := d.plugin.ReceiveKeysplitting(action, actionResponsePayload); err == nil {

		// We need to know the last message for invisible response to keysplitting validation errors
		d.lastMessage = plugin.ActionWrapper{
			Action:        action,
			ActionPayload: returnPayload,
		}

		return d.sendKeysplitting(&ksMessage, action, returnPayload)

	} else {
		d.logger.Error(err)
		return err
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
