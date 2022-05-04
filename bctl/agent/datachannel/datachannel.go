package datachannel

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Masterminds/semver"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/db"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/kube"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/shell"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/web"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	bzerror "bastionzero.com/bctl/v1/bzerolib/error"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type IKeysplitting interface {
	Validate(ksMessage *ksmsg.KeysplittingMessage) error
	BuildAck(ksMessage *ksmsg.KeysplittingMessage, action string, actionPayload []byte) (ksmsg.KeysplittingMessage, error)
}

type IPlugin interface {
	Receive(action string, actionPayload []byte) ([]byte, error)
	Done() <-chan struct{}
	Kill()
}

type DataChannel struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	id string

	websocket    websocket.IWebsocket
	keysplitting IKeysplitting
	plugin       IPlugin

	// incoming and outgoing message channels
	inputChan  chan am.AgentMessage
	outputChan chan am.AgentMessage

	// backward compatability code for when the payload used to come with extra quotes
	payloadClean bool
}

func New(
	parentTmb *tomb.Tomb,
	logger *logger.Logger,
	websocket websocket.IWebsocket,
	keysplitter IKeysplitting,
	id string,
	syn []byte,
) (*DataChannel, error) {

	datachannel := &DataChannel{
		logger:       logger,
		id:           id,
		websocket:    websocket,
		keysplitting: keysplitter,
		inputChan:    make(chan am.AgentMessage, 50),
		outputChan:   make(chan am.AgentMessage, 10),
	}

	// register with websocket so datachannel can send a receive messages
	websocket.Subscribe(id, datachannel)

	// validate the Syn message
	var synPayload ksmsg.KeysplittingMessage
	if err := json.Unmarshal([]byte(syn), &synPayload); err != nil {
		return nil, fmt.Errorf("malformed Keysplitting message: %s", err)
	} else if synPayload.Type != ksmsg.Syn {
		return nil, fmt.Errorf("datachannel must be started with a SYN message")
	}

	// process our syn to startup the plugin
	if err := datachannel.handleKeysplittingMessage(&synPayload); err != nil {
		return nil, err
	}

	// listener for incoming messages
	datachannel.tmb.Go(func() error {
		defer websocket.Unsubscribe(id) // causes decoupling from websocket

		datachannel.tmb.Go(func() error {
			for {
				select {
				case <-datachannel.tmb.Dying():
					return nil
				case agentMessage := <-datachannel.inputChan: // receive messages
					datachannel.processInput(agentMessage)
				}
			}
		})

		for {
			select {
			case <-parentTmb.Dying(): // control channel is dying
				datachannel.plugin.Kill()
				return errors.New("agent was orphaned too young and can't be batman :'(")
			case <-datachannel.tmb.Dying():
				datachannel.plugin.Kill()
				return nil
			case <-datachannel.plugin.Done():
				for {
					// LUCIE: on this side, I do think this strategy is legit
					select {
					// LUCIE: do we only want to send keysplitting messages?
					case agentMessage := <-datachannel.outputChan:
						// Push message to websocket channel output
						datachannel.websocket.Send(agentMessage)
					case <-time.After(1 * time.Second):
						return fmt.Errorf("datachannel's sole plugin is closed")
					}
				}
			case agentMessage := <-datachannel.outputChan:
				// Push message to websocket channel output
				datachannel.websocket.Send(agentMessage)
			}
		}
	})

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
		SchemaVersion:  am.CurrentVersion,
		MessagePayload: messageBytes,
	}

	d.outputChan <- agentMessage
}

func (d *DataChannel) sendKeysplitting(keysplittingMessage *ksmsg.KeysplittingMessage, action string, payload []byte) error {
	// Build and send response
	if respKSMessage, err := d.keysplitting.BuildAck(keysplittingMessage, action, payload); err != nil {
		rerr := fmt.Errorf("could not build response message: %s", err)
		d.logger.Error(rerr)
		return rerr
	} else {
		d.send(am.Keysplitting, respKSMessage)
		return nil
	}
}

func (d *DataChannel) sendError(errType bzerror.ErrorType, err error, hash string) {
	d.logger.Error(err)

	errMsg := bzerror.ErrorMessage{
		SchemaVersion: bzerror.CurrentVersion,
		Timestamp:     time.Now().Unix(),
		Type:          string(errType),
		Message:       err.Error(),
		HPointer:      hash,
	}
	d.send(am.Error, errMsg)
}

func (d *DataChannel) Receive(agentMessage am.AgentMessage) {
	// only push to input channel if we're alive (aka not in the process of dying or already dead)
	if d.tmb.Alive() {
		d.inputChan <- agentMessage
	}
}

func (d *DataChannel) processInput(agentMessage am.AgentMessage) {
	d.logger.Info("received message type: " + agentMessage.MessageType)

	switch am.MessageType(agentMessage.MessageType) {
	case am.Keysplitting:
		var ksMessage ksmsg.KeysplittingMessage
		if err := json.Unmarshal(agentMessage.MessagePayload, &ksMessage); err != nil {
			d.sendError(bzerror.ComponentProcessingError, fmt.Errorf("malformed Keysplitting message: %s", err), "")
		} else {
			d.handleKeysplittingMessage(&ksMessage)
		}
	default:
		rerr := fmt.Errorf("unhandled message type: %s", agentMessage.MessageType)
		d.sendError(bzerror.ComponentProcessingError, rerr, "")
	}
}

func (d *DataChannel) handleKeysplittingMessage(keysplittingMessage *ksmsg.KeysplittingMessage) error {
	if err := d.keysplitting.Validate(keysplittingMessage); err != nil {
		rerr := fmt.Errorf("invalid keysplitting message: %s", err)
		d.sendError(bzerror.KeysplittingValidationError, rerr, keysplittingMessage.Hash())
		return rerr
	}

	switch keysplittingMessage.Type {
	case ksmsg.Syn:
		synPayload := keysplittingMessage.KeysplittingPayload.(ksmsg.SynPayload)

		if d.plugin == nil {
			// Grab user's action
			if parsedAction := strings.Split(synPayload.Action, "/"); len(parsedAction) <= 1 {
				rerr := fmt.Errorf("malformed action: %s", synPayload.Action)
				d.sendError(bzerror.ComponentProcessingError, rerr, keysplittingMessage.Hash())
				return rerr
			} else {
				// Start plugin based on action
				actionPrefix := parsedAction[0]
				if err := d.startPlugin(bzplugin.PluginName(actionPrefix), synPayload.Action, synPayload.ActionPayload, synPayload.SchemaVersion); err != nil {
					d.sendError(bzerror.ComponentStartupError, err, keysplittingMessage.Hash())
					return err
				}
			}
		}

		d.sendKeysplitting(keysplittingMessage, "", []byte{}) // empty payload
	case ksmsg.Data:
		dataPayload := keysplittingMessage.KeysplittingPayload.(ksmsg.DataPayload)

		if d.plugin == nil { // Can't process data message if no plugin created
			rerr := fmt.Errorf("plugin does not exist")
			d.sendError(bzerror.ComponentProcessingError, rerr, keysplittingMessage.Hash())
			return rerr
		}

		// optionally clean our action payload to compensate for old bug that added extra quotes
		actionPayload := dataPayload.ActionPayload
		if !d.payloadClean {
			if cleaned, err := cleanPayload(actionPayload); err != nil {
				return fmt.Errorf("failed to clean payload: %s", err)
			} else {
				actionPayload = cleaned
			}
		}

		// Send message to plugin and catch response action payload
		if returnPayload, err := d.plugin.Receive(dataPayload.Action, actionPayload); err == nil {
			// Build and send response
			d.sendKeysplitting(keysplittingMessage, dataPayload.Action, returnPayload)
		} else {
			rerr := fmt.Errorf("plugin error processing keysplitting message: %s", err)
			d.sendError(bzerror.ComponentProcessingError, rerr, keysplittingMessage.Hash())
		}
	default:
		rerr := fmt.Errorf("invalid Keysplitting Payload")
		d.sendError(bzerror.ComponentProcessingError, rerr, keysplittingMessage.Hash())
		return rerr
	}
	return nil
}

func (d *DataChannel) startPlugin(pluginName bzplugin.PluginName, action string, payload []byte, version string) error {
	d.logger.Infof("Starting %v plugin", pluginName)

	// create channel and listener and pass it to the new plugin
	// TODO: get rid of this and just have an output() in the plugin we can listen to above
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

	subLogger := d.logger.GetPluginLogger(pluginName)

	var err error
	switch pluginName {
	case bzplugin.Kube:
		d.plugin, err = kube.New(subLogger, streamOutputChan, action, payload, version)
	case bzplugin.Db:
		d.plugin, err = db.New(subLogger, streamOutputChan, action, payload, version)
	case bzplugin.Web:
		d.plugin, err = web.New(subLogger, streamOutputChan, action, payload, version)
	case bzplugin.Shell:
		d.plugin, err = shell.New(subLogger, streamOutputChan, action, payload, version)
	default:
		return fmt.Errorf("unrecognized plugin name")
	}

	if err != nil {
		rerr := fmt.Errorf("failed to start %s plugin: %s", pluginName, err)
		d.logger.Error(rerr)
		return rerr
	} else {
		if c, err := semver.NewConstraint(">= 2.0"); err != nil {
			return fmt.Errorf("unable to create versioning constraint")
		} else if v, err := semver.NewVersion(version); err != nil {
			return fmt.Errorf("unable to parse version")
		} else {
			d.payloadClean = c.Check(v)
		}

		d.logger.Infof("%s plugin started!", pluginName)
		return nil
	}
}

func cleanPayload(payload []byte) ([]byte, error) {
	// TODO: CWC-1819: remove once all daemon's are updated
	if len(payload) > 0 {
		payload = payload[1 : len(payload)-1]
	}

	// Json unmarshalling encodes bytes in base64
	if payloadSafe, err := base64.StdEncoding.DecodeString(string(payload)); err != nil {
		return []byte{}, fmt.Errorf("error decoding actionPayload: %s", err)
	} else {
		return payloadSafe, nil
	}
}
