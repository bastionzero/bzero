package datachannel

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/agent/keysplitting"
	"bastionzero.com/bctl/v1/bctl/agent/plugin"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/kube"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	rrr "bastionzero.com/bctl/v1/bzerolib/error"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// Plugins this datachannel accepts
type PluginName string

const (
	Kube PluginName = "kube"
)

type DataChannel struct {
	websocket *websocket.Websocket
	logger    *logger.Logger
	tmb       tomb.Tomb
	id        string

	plugin       plugin.IPlugin
	keysplitting keysplitting.IKeysplitting

	// incoming and outgoing message channels
	inputChan chan am.AgentMessage
}

func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	websocket *websocket.Websocket,
	id string,
	syn []byte) (*DataChannel, error) {

	keysplitter, err := keysplitting.New()
	if err != nil {
		logger.Error(err)
		return &DataChannel{}, err
	}

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
		defer datachannel.logger.Infof("Datachannel closed")
		for {
			select {
			case <-parentTmb.Dying(): // control channel is dying
				return errors.New("agent was orphaned too young and can't be batman :'(")
			case <-datachannel.tmb.Dying():
				time.Sleep(30 * time.Second) // allow the datachannel to close gracefully TODO: Figure out a better way to gracefully die
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
		return &DataChannel{}, rerr
	} else if synPayload.Type != ksmsg.Syn {
		err := fmt.Errorf("datachannel must be started with a SYN message")
		logger.Error(err)
		return &DataChannel{}, err
	}

	// process our syn to startup the plugin
	datachannel.handleKeysplittingMessage(&synPayload)

	return datachannel, nil
}

func (d *DataChannel) Close(reason error) {
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
	d.inputChan <- agentMessage
}

func (d *DataChannel) processInput(agentMessage am.AgentMessage) {
	d.logger.Info("received message type: " + agentMessage.MessageType)

	switch am.MessageType(agentMessage.MessageType) {
	case am.Keysplitting:
		var ksMessage ksmsg.KeysplittingMessage
		if err := json.Unmarshal(agentMessage.MessagePayload, &ksMessage); err != nil {
			rerr := fmt.Errorf("malformed Keysplitting message")
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
			d.sendError(rrr.KeysplittingExecutionError, rerr)
			return
		} else {
			if d.plugin != nil { // Don't start plugin if there's already one started
				return
			}

			// Start plugin based on action
			actionPrefix := parsedAction[0]
			switch actionPrefix {
			case "kube":
				if err := d.startPlugin(PluginName(parsedAction[0]), synPayload.ActionPayload); err != nil {
					d.sendError(rrr.ComponentStartupError, err)
					return
				}
			default:
				actionErr := fmt.Errorf("unhandled action prefix: %s", actionPrefix)
				d.logger.Error(actionErr)
				d.sendError(rrr.KeysplittingExecutionError, actionErr)
				return
			}

			d.sendKeysplitting(keysplittingMessage, "", []byte{}) // empty payload
		}
	case ksmsg.Data:
		dataPayload := keysplittingMessage.KeysplittingPayload.(ksmsg.DataPayload)

		// Send message to plugin and catch response action payload
		if _, returnPayload, err := d.plugin.Receive(dataPayload.Action, dataPayload.ActionPayload); err == nil {

			// Build and send response
			d.sendKeysplitting(keysplittingMessage, dataPayload.Action, returnPayload)
		} else {
			rerr := fmt.Errorf("plugin error processing keysplitting message: %s", err)
			d.sendError(rrr.KeysplittingExecutionError, rerr)
		}
	default:
		rerr := fmt.Errorf("invalid Keysplitting Payload")
		d.sendError(rrr.KeysplittingExecutionError, rerr)
	}
}

func (d *DataChannel) startPlugin(pluginName PluginName, payload []byte) error {
	d.logger.Infof("Starting %v plugin", pluginName)

	switch pluginName {
	case Kube:
		// create channel and listener and pass it to the new plugin
		streamOutputChan := make(chan smsg.StreamMessage, 20)
		go func() {
			for {
				select {
				case <-d.tmb.Dying():
					return
				case streamMessage := <-streamOutputChan:
					d.logger.Infof("Sending %s stream message", streamMessage.Type)
					d.send(am.Stream, streamMessage)
				}
			}
		}()

		subLogger := d.logger.GetPluginLogger(string(pluginName))
		if plugin, err := kube.New(&d.tmb, subLogger, streamOutputChan, payload); err != nil {
			return err
		} else {
			d.plugin = plugin
		}

		d.logger.Infof("%s plugin started!", pluginName)
	default:
		return fmt.Errorf("tried to start an unrecognized plugin: %s", pluginName)
	}
	return nil
}
