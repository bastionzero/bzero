package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/web/actions/webdial"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/web/actions/webwebsocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzweb "bastionzero.com/bctl/v1/bzerolib/plugin/web"
	bzwebdial "bastionzero.com/bctl/v1/bzerolib/plugin/web/actions/webdial"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// Perhaps unnecessary but it is nice to make sure that each action is implementing a common function set
type IWebDaemonAction interface {
	ReceiveKeysplitting(wrappedAction plugin.ActionWrapper)
	ReceiveStream(stream smsg.StreamMessage)
	Start(tmb *tomb.Tomb, Writer http.ResponseWriter, Request *http.Request) error
}

type WebDaemonPlugin struct {
	tmb    *tomb.Tomb
	logger *logger.Logger

	// Input and output channels
	streamInputChan chan smsg.StreamMessage
	outputQueue     chan plugin.ActionWrapper

	actions       map[string]IWebDaemonAction
	actionMapLock sync.RWMutex // for keeping the action map thread-safe

	// Web-specific vars
	remoteHost     string
	remotePort     int
	sequenceNumber int
}

func New(parentTmb *tomb.Tomb, logger *logger.Logger, actionParams bzweb.WebActionParams) (*WebDaemonPlugin, error) {
	plugin := WebDaemonPlugin{
		tmb:             parentTmb,
		logger:          logger,
		streamInputChan: make(chan smsg.StreamMessage, 25),
		sequenceNumber:  0,
		outputQueue:     make(chan plugin.ActionWrapper, 25),
		actions:         make(map[string]IWebDaemonAction),
		remoteHost:      actionParams.RemoteHost,
		remotePort:      actionParams.RemotePort,
	}

	// listener for processing any incoming stream messages, since they are not treated as part of
	// the keysplitting synchronous chain
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

func (k *WebDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	k.logger.Debugf("Web dial action received %v stream", smessage.Type)
	k.streamInputChan <- smessage
}

func (k *WebDaemonPlugin) processStream(smessage smsg.StreamMessage) error {
	// find action by requestid in map and push stream message to it
	if act, ok := k.getActionsMap(smessage.RequestId); ok {
		act.ReceiveStream(smessage)
		return nil
	}

	rerr := fmt.Errorf("unknown request ID: %v. This is expected if the action has already been completed", smessage.RequestId)
	k.logger.Error(rerr)
	return rerr
}

func (k *WebDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) (string, []byte, error) {
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

func (w *WebDaemonPlugin) processKeysplitting(action string, actionPayload []byte) error {

	// we only care about a single action right now
	if action == string(bzwebdial.WebDialInterrupt) {
		var webInterrupt bzwebdial.WebInterruptActionPayload
		if err := json.Unmarshal(actionPayload, &webInterrupt); err != nil {
			return fmt.Errorf("could not unmarshal json: %s", err)
		} else {

			// push the keysplitting message to the action
			if act, ok := w.getActionsMap(webInterrupt.RequestId); ok {
				act.ReceiveKeysplitting(plugin.ActionWrapper{
					Action:        action,
					ActionPayload: actionPayload,
				})
				return nil
			}
		}
	}
	return nil
}

func (w *WebDaemonPlugin) Feed(food interface{}) error {
	// Make sure food matches what it says on the label
	webFood, ok := food.(bzweb.WebFood)
	if !ok {
		return fmt.Errorf("we asked for an entremet not a sloppy joe") // couldn't think of a neater food that was less pretentious
	}
	// Always generate a requestId, each new web command is its own request
	requestId := uuid.New().String()

	// Create action logger
	actLogger := w.logger.GetActionLogger(string(webFood.Action))
	actLogger.AddRequestId(requestId)

	var act IWebDaemonAction
	var actOutputChan chan plugin.ActionWrapper

	switch webFood.Action {
	case bzweb.Dial:
		act, actOutputChan = webdial.New(actLogger, requestId)
	case bzweb.Websocket:
		act, actOutputChan = webwebsocket.New(actLogger, requestId)
	default:
		rerr := fmt.Errorf("unrecognized web action: %v", string(webFood.Action))
		w.logger.Error(rerr)
		return rerr
	}

	// add the action to the action map for future interaction
	w.updateActionsMap(act, requestId)

	// listen to action output channel, remove action from map if channel is closed
	go func() {
		for {
			select {
			case <-w.tmb.Dying():
				return
			case m, more := <-actOutputChan:
				if more {
					w.outputQueue <- m
				} else {
					w.deleteActionsMap(requestId)
					w.tmb.Kill(fmt.Errorf("killing the action only a mother plugin could ever love"))
					return
				}
			}
		}
	}()

	w.logger.Infof("Created %s action with requestId %v", string(webFood.Action), requestId)

	// send local tcp connection to action
	if err := act.Start(w.tmb, webFood.Writer, webFood.Request); err != nil {
		w.logger.Error(fmt.Errorf("%s error: %s", string(webFood.Action), err))
	}

	return nil
}

func (k *WebDaemonPlugin) updateActionsMap(newAction IWebDaemonAction, id string) {
	// Helper function so we avoid writing to this map at the same time
	k.actionMapLock.Lock()
	defer k.actionMapLock.Unlock()

	k.actions[id] = newAction
}

func (k *WebDaemonPlugin) deleteActionsMap(rid string) {
	k.actionMapLock.Lock()
	defer k.actionMapLock.Unlock()

	delete(k.actions, rid)
}

func (k *WebDaemonPlugin) getActionsMap(rid string) (IWebDaemonAction, bool) {
	k.actionMapLock.Lock()
	defer k.actionMapLock.Unlock()

	act, ok := k.actions[rid]
	return act, ok
}
