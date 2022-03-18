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

func (w *WebDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	w.logger.Debugf("Web dial action received %v stream", smessage.Type)
	w.streamInputChan <- smessage
}

func (w *WebDaemonPlugin) processStream(smessage smsg.StreamMessage) error {
	// find action by requestid in map and push stream message to it
	if act, ok := w.getActionsMap(smessage.RequestId); ok {
		act.ReceiveStream(smessage)
	}

	w.logger.Tracef("unknown request ID: %v. This is expected if the action has already been completed", smessage.RequestId)
	return nil
}

func (w *WebDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) (string, []byte, error) {
	// First, process the incoming message
	if err := w.processKeysplitting(action, actionPayload); err != nil {
		return "", []byte{}, err
	}

	// Now that we've received, we wait for any new outgoing commands.  Because the existence of any
	// such command is dependent on the user, there may not be one waiting so we wait for it.
	w.logger.Info("Waiting for input...")

	select {
	case <-w.tmb.Dying():
		return "", []byte{}, nil
	case actionMessage := <-w.outputQueue: // some action's got something to say
		w.logger.Infof("Sending input from action: %v", actionMessage.Action)

		// turn the actionPayload into bytes and return it
		if actionPayloadBytes, err := json.Marshal(actionMessage.ActionPayload); err != nil {
			w.logger.Infof("actionPayload: %+v", actionPayload)
			return "", []byte{}, fmt.Errorf("could not marshal actionPayload json: %s", err)
		} else {
			return actionMessage.Action, actionPayloadBytes, nil
		}
	}
}

func (w *WebDaemonPlugin) processKeysplitting(action string, actionPayload []byte) error {
	// the only keysplitting message that we would receive is the ack for our web action interrupt
	// we don't do anything with it on the daemon side, so we receive it here and it will get logged
	// but no particular action will be taken
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
					w.tmb.Kill(fmt.Errorf("killing web action with request id: %s", requestId))
					return
				}
			}
		}
	}()

	w.logger.Infof("Web plugin created a %s action with requestId %v", string(webFood.Action), requestId)

	// send local tcp connection to action
	if err := act.Start(w.tmb, webFood.Writer, webFood.Request); err != nil {
		w.logger.Error(fmt.Errorf("%s error: %s", string(webFood.Action), err))
	}

	return nil
}

func (w *WebDaemonPlugin) updateActionsMap(newAction IWebDaemonAction, id string) {
	// Helper function so we avoid writing to this map at the same time
	w.actionMapLock.Lock()
	defer w.actionMapLock.Unlock()

	w.actions[id] = newAction
}

func (w *WebDaemonPlugin) deleteActionsMap(rid string) {
	w.actionMapLock.Lock()
	defer w.actionMapLock.Unlock()

	delete(w.actions, rid)
}

func (w *WebDaemonPlugin) getActionsMap(rid string) (IWebDaemonAction, bool) {
	w.actionMapLock.Lock()
	defer w.actionMapLock.Unlock()

	act, ok := w.actions[rid]
	return act, ok
}
