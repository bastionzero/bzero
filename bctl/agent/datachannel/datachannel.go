package datachannel

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/agent/mrzap"
	"bastionzero.com/bctl/v1/bctl/agent/plugin"
	db "bastionzero.com/bctl/v1/bctl/agent/plugin/db"
	kube "bastionzero.com/bctl/v1/bctl/agent/plugin/kube"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/web"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	rrr "bastionzero.com/bctl/v1/bzerolib/error"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	mzmsg "bastionzero.com/bctl/v1/bzerolib/mrzap/message"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// Plugins this datachannel accepts
type PluginName string

const (
	Kube PluginName = "kube"
	Db   PluginName = "db"
	Web  PluginName = "web"
)

type DataChannel struct {
	websocket *websocket.Websocket
	logger    *logger.Logger
	tmb       tomb.Tomb
	id        string

	plugin plugin.IPlugin
	zapper mrzap.IMrZAP

	// incoming and outgoing message channels
	inputChan chan am.AgentMessage
}

func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	websocket *websocket.Websocket,
	id string,
	syn []byte) (*DataChannel, error) {

	zapper, err := mrzap.New()
	if err != nil {
		logger.Error(err)
		return &DataChannel{}, err
	}

	datachannel := &DataChannel{
		websocket: websocket,
		logger:    logger,
		zapper:    zapper,
		inputChan: make(chan am.AgentMessage, 50),
		id:        id,
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
	var synPayload mzmsg.MrZAPMessage
	if err := json.Unmarshal([]byte(syn), &synPayload); err != nil {
		rerr := fmt.Errorf("malformed MrZAP message")
		logger.Error(rerr)
		return &DataChannel{}, rerr
	} else if synPayload.Type != mzmsg.Syn {
		err := fmt.Errorf("datachannel must be started with a SYN message")
		logger.Error(err)
		return &DataChannel{}, err
	}

	// process our syn to startup the plugin
	datachannel.handleMrZAPMessage(&synPayload)

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

func (d *DataChannel) sendMrZAP(mrzapMessage *mzmsg.MrZAPMessage, action string, payload []byte) error {
	// Build and send response
	if respMZMessage, err := d.zapper.BuildResponse(mrzapMessage, action, payload); err != nil {
		rerr := fmt.Errorf("could not build response message: %s", err)
		d.logger.Error(rerr)
		return rerr
	} else {
		d.send(am.MrZAP, respMZMessage)
		return nil
	}
}

func (d *DataChannel) sendError(errType rrr.ErrorType, err error) {
	d.logger.Error(err)
	errMsg := rrr.ErrorMessage{
		Type:     string(errType),
		Message:  err.Error(),
		HPointer: d.zapper.GetHpointer(),
	}
	d.send(am.Error, errMsg)
}

func (d *DataChannel) Receive(agentMessage am.AgentMessage) {
	d.inputChan <- agentMessage
}

func (d *DataChannel) processInput(agentMessage am.AgentMessage) {
	d.logger.Info("received message type: " + agentMessage.MessageType)

	switch am.MessageType(agentMessage.MessageType) {
	case am.MrZAP:
		var ksMessage mzmsg.MrZAPMessage
		if err := json.Unmarshal(agentMessage.MessagePayload, &ksMessage); err != nil {
			rerr := fmt.Errorf("malformed MrZAP message")
			d.sendError(rrr.MrZAPValidationError, rerr)
		} else {
			d.handleMrZAPMessage(&ksMessage)
		}
	default:
		rerr := fmt.Errorf("unhandled message type: %v", agentMessage.MessageType)
		d.sendError(rrr.ComponentProcessingError, rerr)
	}
}

func (d *DataChannel) handleMrZAPMessage(mrzapMessage *mzmsg.MrZAPMessage) {
	if err := d.zapper.Validate(mrzapMessage); err != nil {
		rerr := fmt.Errorf("invalid mrzap message: %s", err)
		d.sendError(rrr.MrZAPValidationError, rerr)
		return
	}

	switch mrzapMessage.Type {
	case mzmsg.Syn:
		synPayload := mrzapMessage.MrZAPPayload.(mzmsg.SynPayload)
		// Grab user's action
		if parsedAction := strings.Split(synPayload.Action, "/"); len(parsedAction) <= 1 {
			rerr := fmt.Errorf("malformed action: %s", synPayload.Action)
			d.sendError(rrr.MrZAPExecutionError, rerr)
			return
		} else {
			if d.plugin != nil { // Don't start plugin if there's already one started
				return
			}

			// Start plugin based on action
			actionPrefix := parsedAction[0]
			if err := d.startPlugin(PluginName(actionPrefix), synPayload.ActionPayload); err != nil {
				d.sendError(rrr.ComponentStartupError, err)
				return
			}
			d.sendMrZAP(mrzapMessage, "", []byte{}) // empty payload
		}
	case mzmsg.Data:
		dataPayload := mrzapMessage.MrZAPPayload.(mzmsg.DataPayload)

		// Send message to plugin and catch response action payload
		if _, returnPayload, err := d.plugin.Receive(dataPayload.Action, dataPayload.ActionPayload); err == nil {

			// Build and send response
			d.sendMrZAP(mrzapMessage, dataPayload.Action, returnPayload)
		} else {
			rerr := fmt.Errorf("plugin error processing mrzap message: %s", err)
			d.sendError(rrr.MrZAPExecutionError, rerr)
		}
	default:
		rerr := fmt.Errorf("invalid MrZAP Payload")
		d.sendError(rrr.MrZAPExecutionError, rerr)
	}
}

func (d *DataChannel) startPlugin(pluginName PluginName, payload []byte) error {
	d.logger.Infof("Starting %v plugin", pluginName)

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

	var plugin plugin.IPlugin
	var err error

	switch pluginName {
	case Kube:
		plugin, err = kube.New(&d.tmb, subLogger, streamOutputChan, payload)
	case Db:
		plugin, err = db.New(&d.tmb, subLogger, streamOutputChan, payload)
	case Web:
		plugin, err = web.New(&d.tmb, subLogger, streamOutputChan, payload)
	default:
		return fmt.Errorf("tried to start an unrecognized plugin: %s", pluginName)
	}

	if err != nil {
		return err
	} else {
		d.plugin = plugin
	}

	d.logger.Infof("%s plugin started!", pluginName)

	return nil
}
