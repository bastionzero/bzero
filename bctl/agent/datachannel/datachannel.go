package datachannel

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/tomb.v2"

	plgn "bastionzero.com/bctl/v1/bctl/agent/plugin"
	db "bastionzero.com/bctl/v1/bctl/agent/plugin/db"
	kube "bastionzero.com/bctl/v1/bctl/agent/plugin/kube"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/shell"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/web"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	rrr "bastionzero.com/bctl/v1/bzerolib/error"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type IKeysplitting interface {
	GetHpointer() string
	Validate(ksMessage *ksmsg.KeysplittingMessage) error
	BuildResponse(ksMessage *ksmsg.KeysplittingMessage, action string, actionPayload []byte) (ksmsg.KeysplittingMessage, error)
}

type DataChannel struct {
	websocket websocket.IWebsocket
	logger    *logger.Logger
	tmb       tomb.Tomb
	id        string

	plugin       plgn.IPlugin
	keysplitting IKeysplitting

	// incoming and outgoing message channels
	inputChan chan am.AgentMessage
}

func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	websocket websocket.IWebsocket,
	id string,
	syn []byte,
	keysplitter IKeysplitting) (*DataChannel, error) {

	datachannel := &DataChannel{
		websocket:    websocket,
		logger:       logger,
		keysplitting: keysplitter,
		inputChan:    make(chan am.AgentMessage, 50),
		id:           id,
	}

	// register with websocket so datachannel can send a receive messages
	websocket.Subscribe(id, datachannel)

	// listener for incoming messages
	datachannel.tmb.Go(func() error {
		defer websocket.Unsubscribe(id) // causes decoupling from websocket
		for {
			select {
			case <-parentTmb.Dying(): // control channel is dying
				return errors.New("agent was orphaned too young and can't be batman :'(")
			case <-datachannel.tmb.Dying():
				time.Sleep(10 * time.Second) // allow the datachannel to close gracefully TODO: Figure out a better way to gracefully die
				return nil
			case agentMessage := <-datachannel.inputChan: // receive messages
				datachannel.processInput(agentMessage)
			}
		}
	})

	// validate the Syn message
	var synPayload ksmsg.KeysplittingMessage
	if err := json.Unmarshal([]byte(syn), &synPayload); err != nil {
		rerr := fmt.Errorf("malformed Keysplitting message")
		logger.Error(rerr)
		return nil, rerr
	} else if synPayload.Type != ksmsg.Syn {
		err := fmt.Errorf("datachannel must be started with a SYN message")
		logger.Error(err)
		return nil, err
	}

	// process our syn to startup the plugin
	datachannel.handleKeysplittingMessage(&synPayload)

	return datachannel, nil
}

func (d *DataChannel) Close(reason error) {
	d.logger.Infof("Datachannel closed because: %s", reason)
	d.tmb.Kill(reason) // kills all datachannel, plugin, and action goroutines
	d.tmb.Wait()
}

// Wraps and sends the payload
func (d *DataChannel) send(messageType am.MessageType, messagePayload interface{}) {
	messageBytes, _ := json.Marshal(messagePayload)
	agentMessage := am.AgentMessage{
		ChannelId:      d.id,
		MessageType:    string(messageType),
		SchemaVersion:  am.SchemaVersion,
		MessagePayload: messageBytes,
	}

	// Push message to websocket channel output
	d.websocket.Send(agentMessage)
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

func (d *DataChannel) sendError(errType rrr.ErrorType, err error) {
	d.logger.Error(err)
	errMsg := rrr.ErrorMessage{
		Type:     string(errType),
		Message:  err.Error(),
		HPointer: d.keysplitting.GetHpointer(),
	}
	d.send(am.Error, errMsg)
}

func (d *DataChannel) Receive(agentMessage am.AgentMessage) {
	// only push to input channel if we're alive (aka not in the process of dying or already dead)
	if d.tmb.Err() == tomb.ErrStillAlive {
		d.inputChan <- agentMessage
	}
}

func (d *DataChannel) processInput(agentMessage am.AgentMessage) {
	d.logger.Info("received message type: " + agentMessage.MessageType)

	switch am.MessageType(agentMessage.MessageType) {
	case am.Keysplitting:
		var ksMessage ksmsg.KeysplittingMessage
		if err := json.Unmarshal(agentMessage.MessagePayload, &ksMessage); err != nil {
			rerr := fmt.Errorf("malformed Keysplitting message: %s", err)
			d.sendError(rrr.KeysplittingValidationError, rerr)
		} else {
			d.handleKeysplittingMessage(&ksMessage)
		}
	default:
		rerr := fmt.Errorf("unhandled message type: %v", agentMessage.MessageType)
		d.sendError(rrr.ComponentProcessingError, rerr)
	}
}

func (d *DataChannel) handleKeysplittingMessage(keysplittingMessage *ksmsg.KeysplittingMessage) {
	if err := d.keysplitting.Validate(keysplittingMessage); err != nil {
		rerr := fmt.Errorf("invalid keysplitting message: %s", err)
		d.sendError(rrr.KeysplittingValidationError, rerr)
		return
	}

	switch keysplittingMessage.Type {
	case ksmsg.Syn:
		synPayload := keysplittingMessage.KeysplittingPayload.(ksmsg.SynPayload)

		// Grab user's action
		if parsedAction := strings.Split(synPayload.Action, "/"); len(parsedAction) <= 1 {
			rerr := fmt.Errorf("malformed action: %s", synPayload.Action)
			d.sendError(rrr.ComponentProcessingError, rerr)
			return
		} else {

			// Don't start plugin if there's already one started
			if d.plugin == nil {

				// Start plugin based on action
				actionPrefix := parsedAction[0]
				if err := d.startPlugin(bzplugin.PluginName(actionPrefix), synPayload.Action, synPayload.ActionPayload); err != nil {
					d.sendError(rrr.ComponentStartupError, err)
					return
				}
			}

			// return a SYN/ACK
			d.sendKeysplitting(keysplittingMessage, "", []byte{}) // empty payload
		}
	case ksmsg.Data:
		dataPayload := keysplittingMessage.KeysplittingPayload.(ksmsg.DataPayload)

		if d.plugin == nil { // Can't process data message if no plugin created
			rerr := fmt.Errorf("plugin does not exist")
			d.sendError(rrr.ComponentProcessingError, rerr)
			return
		}

		// Send message to plugin and catch response action payload
		// TODO: if we're ignoring the action we should really not be sending it up here
		if _, returnPayload, err := d.plugin.Receive(dataPayload.Action, dataPayload.ActionPayload); err == nil {

			// Build and send response
			d.sendKeysplitting(keysplittingMessage, dataPayload.Action, returnPayload)
		} else {
			rerr := fmt.Errorf("plugin error processing keysplitting message: %s", err)
			d.sendError(rrr.ComponentProcessingError, rerr)
		}
	default:
		rerr := fmt.Errorf("invalid Keysplitting Payload")
		d.sendError(rrr.ComponentProcessingError, rerr)
	}
}

func (d *DataChannel) startPlugin(pluginName bzplugin.PluginName, action string, payload []byte) error {
	d.logger.Infof("Starting %v plugin", pluginName)

	// create channel and listener and pass it to the new plugin
	streamOutputChan := make(chan smsg.StreamMessage, 30)
	go func() {
		for {
			select {
			case <-d.tmb.Dying():
				return
			case streamMessage := <-streamOutputChan:
				d.logger.Infof("Sending %s - %s - %t stream message", streamMessage.Action, streamMessage.Type, streamMessage.More)
				d.send(am.Stream, streamMessage)
			}
		}
	}()

	subLogger := d.logger.GetPluginLogger(string(pluginName))

	// var plugin plgn.IPlugin
	var err error
	switch pluginName {
	case bzplugin.Kube:
		d.plugin, err = kube.New(&d.tmb, subLogger, streamOutputChan, payload)
	case bzplugin.Db:
		d.plugin, err = db.New(&d.tmb, subLogger, streamOutputChan, action, payload)
	case bzplugin.Web:
		d.plugin, err = web.New(&d.tmb, subLogger, streamOutputChan, action, payload)
	case bzplugin.Shell:
		d.plugin, err = shell.New(&d.tmb, subLogger, streamOutputChan, action, payload)
	default:
		return fmt.Errorf("unrecognized plugin name")
	}

	if err != nil {
		rerr := fmt.Errorf("failed to start %s plugin: %s", pluginName, err)
		d.logger.Error(rerr)
		return rerr
	} else {
		d.logger.Infof("%s plugin started!", pluginName)
		return nil
	}
}
