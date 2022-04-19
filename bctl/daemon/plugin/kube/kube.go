package kube

import (
	"encoding/json"
	"fmt"
	"net/http"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/actions/exec"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/actions/portforward"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/actions/restapi"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/actions/stream"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzkube "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"github.com/google/uuid"
	"gopkg.in/tomb.v2"
)

type JustRequestId struct {
	RequestId string `json:"requestId"`
}
type KubeFood struct {
	Action  bzkube.KubeAction
	LogId   string
	Command string
	Writer  http.ResponseWriter
	Reader  *http.Request
}

// Perhaps unnecessary but it is nice to make sure that each action is implementing a common function set
type IKubeDaemonAction interface {
	ReceiveKeysplitting(wrappedAction plugin.ActionWrapper)
	ReceiveStream(stream smsg.StreamMessage)
	Start(tmb *tomb.Tomb, writer http.ResponseWriter, request *http.Request) error
}
type KubeDaemonPlugin struct {
	tmb    *tomb.Tomb
	logger *logger.Logger
	// Input and output channels
	streamInputChan chan smsg.StreamMessage
	outputQueue     chan plugin.ActionWrapper

	action IKubeDaemonAction

	// Kube-specific vars
	targetUser   string
	targetGroups []string
}

func New(parentTmb *tomb.Tomb, logger *logger.Logger, actionParams bzkube.KubeActionParams) (*KubeDaemonPlugin, error) {
	plugin := KubeDaemonPlugin{
		tmb:             parentTmb,
		logger:          logger,
		streamInputChan: make(chan smsg.StreamMessage, 25),
		outputQueue:     make(chan plugin.ActionWrapper, 25),
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
	if k.action != nil {
		k.action.ReceiveStream(smessage)
		return nil
	} else {
		return fmt.Errorf("db plugin received stream message before an action was created. Ignoring")
	}
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
		wrappedAction := plugin.ActionWrapper{
			Action:        action,
			ActionPayload: actionPayload,
		}
		k.action.ReceiveKeysplitting(wrappedAction)

	}
	return nil
}
func (k *KubeDaemonPlugin) Feed(food interface{}) error {
	// Check food matches nutrition label
	kubeFood, ok := food.(KubeFood)
	if !ok {
		return fmt.Errorf("food did not reach expected nutrition: %+v", food)
	}

	// Always generate a requestId, each new kube command is its own request
	// FIXME: not for long...
	requestId := uuid.New().String()

	// Create action logger
	actLogger := k.logger.GetActionLogger(string(kubeFood.Action))
	actLogger.AddRequestId(requestId)

	var actOutputChan chan plugin.ActionWrapper

	switch kubeFood.Action {
	case bzkube.Exec:
		k.action, actOutputChan = exec.New(actLogger, requestId, kubeFood.LogId, kubeFood.Command)
	case bzkube.Stream:
		k.action, actOutputChan = stream.New(actLogger, requestId, kubeFood.LogId, kubeFood.Command)
	case bzkube.RestApi:
		k.action, actOutputChan = restapi.New(actLogger, requestId, kubeFood.LogId, kubeFood.Command)
	case bzkube.PortForward:
		k.action, actOutputChan = portforward.New(actLogger, requestId, kubeFood.LogId, kubeFood.Command)
	default:
		rerr := fmt.Errorf("unrecognized kubectl action: %v", string(kubeFood.Action))
		k.logger.Error(rerr)
		return rerr
	}

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
					k.action = nil

					// Don't kill rest api actions, we batch those
					if kubeFood.Action != bzkube.RestApi {
						k.tmb.Kill(nil)
					}
					return
				}
			}
		}
	}()
	k.logger.Infof("Created %s action with url %s and requestId %v", string(kubeFood.Action), kubeFood.Reader.URL.Path, requestId)

	// send http handlers to action
	if err := k.action.Start(k.tmb, kubeFood.Writer, kubeFood.Reader); err != nil {
		k.logger.Error(fmt.Errorf("%s error: %s", string(kubeFood.Action), err))
	}
	return nil
}
