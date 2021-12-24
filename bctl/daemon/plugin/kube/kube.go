package kube

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/actions/exec"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/actions/portforward"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/actions/restapi"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/actions/stream"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type JustRequestId struct {
	RequestId string `json:"requestId"`
}

type KubeDaemonAction string

const (
	Exec        KubeDaemonAction = "exec"
	Stream      KubeDaemonAction = "stream"
	RestApi     KubeDaemonAction = "restapi"
	PortForward KubeDaemonAction = "portforward"
)

// Perhaps unnecessary but it is nice to make sure that each action is implementing a common function set
type IKubeDaemonAction interface {
	ReceiveKeysplitting(wrappedAction plugin.ActionWrapper)
	ReceiveStream(stream smsg.StreamMessage)
	Start(tmb *tomb.Tomb, writer http.ResponseWriter, request *http.Request) error
}

type KubeActionParams struct {
	TargetUser   string   `json:"targetUser"`
	TargetGroups []string `json:"targetGroups"`
}

type KubeDaemonPlugin struct {
	tmb    *tomb.Tomb
	logger *logger.Logger

	// Input and output channels
	streamInputChan chan smsg.StreamMessage
	outputQueue     chan plugin.ActionWrapper

	actions       map[string]IKubeDaemonAction
	actionMapLock sync.RWMutex // for keeping the action map thread-safe

	// Kube-specific vars
	targetUser   string
	targetGroups []string
}

func New(parentTmb *tomb.Tomb, logger *logger.Logger, actionParams KubeActionParams) (*KubeDaemonPlugin, error) {
	plugin := KubeDaemonPlugin{
		tmb:             parentTmb,
		logger:          logger,
		actionMapLock:   sync.RWMutex{},
		streamInputChan: make(chan smsg.StreamMessage, 25),
		outputQueue:     make(chan plugin.ActionWrapper, 25),
		actions:         make(map[string]IKubeDaemonAction),
		targetUser:      actionParams.TargetUser,
		targetGroups:    actionParams.TargetGroups,
	}

	// listener for processing any incoming stream messages, since they are not treated as part of
	// the keysplitting syncronous chain
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

func (k *KubeDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	k.streamInputChan <- smessage
}

func (k *KubeDaemonPlugin) processStream(smessage smsg.StreamMessage) error {
	// find action by requestid in map and push stream message to it
	if act, ok := k.getActionsMap(smessage.RequestId); ok {
		act.ReceiveStream(smessage)
		return nil
	}

	rerr := fmt.Errorf("unknown request ID: %v", smessage.RequestId)
	k.logger.Error(rerr)
	return rerr
}

func (k *KubeDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) (string, []byte, error) {
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

func (k *KubeDaemonPlugin) processKeysplitting(action string, actionPayload []byte) error {
	// if actionPayload is empty, then there's nothing we need to process
	if len(actionPayload) == 0 {
		return nil
	}

	// Get just the request ID so we can associate it with the previously started action object
	var d JustRequestId
	if err := json.Unmarshal(actionPayload, &d); err != nil {
		rerr := fmt.Errorf("could not unmarshal actionPayload json: %s", err)
		k.logger.Infof("actionPayload: %s", string(actionPayload))
		k.logger.Error(rerr)
		return rerr
	} else {

		// Lookup action in thread-safe map and push the keysplitting message to it
		if act, ok := k.getActionsMap(d.RequestId); ok {
			wrappedAction := plugin.ActionWrapper{
				Action:        action,
				ActionPayload: actionPayload,
			}
			act.ReceiveKeysplitting(wrappedAction)

			// If the action doesn't exist, then return an error
		} else {
			rerr := fmt.Errorf("unknown request ID: %v", d.RequestId)
			k.logger.Error(rerr)
			return rerr
		}
	}
	return nil
}

func (k *KubeDaemonPlugin) Feed(action string, logId string, command string, w http.ResponseWriter, r *http.Request) error {
	// Always generate a requestId, each new kube command is its own request
	requestId := uuid.New().String()

	// Create action logger
	actLogger := k.logger.GetActionLogger(action)
	actLogger.AddRequestId(requestId)

	var act IKubeDaemonAction
	var actOutputChan chan plugin.ActionWrapper

	switch KubeDaemonAction(action) {
	case Exec:
		act, actOutputChan = exec.New(actLogger, requestId, logId, command)
	case Stream:
		act, actOutputChan = stream.New(actLogger, requestId, logId, command)
	case RestApi:
		act, actOutputChan = restapi.New(actLogger, requestId, logId, command)
	case PortForward:
		act, actOutputChan = portforward.New(actLogger, requestId, logId, command)
	default:
		rerr := fmt.Errorf("unrecognized kubectl action: %v", string(action))
		k.logger.Error(rerr)
		return rerr
	}

	// add the action to the action map for future interaction
	k.updateActionsMap(act, requestId)

	// listen to action output channel, remove action from map if channel is closed
	go func() {
		for {
			select {
			case <-k.tmb.Dying():
				return
			case m, more := <-actOutputChan:
				if more {
					k.outputQueue <- m
				} else {
					k.deleteActionsMap(requestId)

					// Don't kill rest api actions, we batch those
					if KubeDaemonAction(action) != RestApi {
						k.tmb.Kill(nil)
					}
					return
				}
			}
		}
	}()

	k.logger.Infof("Created %s action with requestId %v", string(action), requestId)

	// send http handlers to action
	if err := act.Start(k.tmb, w, r); err != nil {
		k.logger.Error(fmt.Errorf("%s error: %s", string(action), err))
	}
	return nil
}

func (k *KubeDaemonPlugin) updateActionsMap(newAction IKubeDaemonAction, id string) {
	// Helper function so we avoid writing to this map at the same time
	k.actionMapLock.Lock()
	defer k.actionMapLock.Unlock()

	k.actions[id] = newAction
}

func (k *KubeDaemonPlugin) deleteActionsMap(rid string) {
	k.actionMapLock.Lock()
	defer k.actionMapLock.Unlock()

	delete(k.actions, rid)
}

func (k *KubeDaemonPlugin) getActionsMap(rid string) (IKubeDaemonAction, bool) {
	k.actionMapLock.Lock()
	defer k.actionMapLock.Unlock()

	act, ok := k.actions[rid]
	return act, ok
}
