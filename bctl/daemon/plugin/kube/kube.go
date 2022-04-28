package kube

import (
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
)

type IKubeDaemonAction interface {
	ReceiveKeysplitting(actionPayload []byte)
	ReceiveStream(stream smsg.StreamMessage)
	Start(writer http.ResponseWriter, request *http.Request) error
	Kill()
}
type KubeDaemonPlugin struct {
	logger *logger.Logger

	action   IKubeDaemonAction
	doneChan chan struct{}
	killed   bool

	// outbox channel
	outputQueue chan plugin.ActionWrapper

	// Kube-specific vars
	targetUser   string
	targetGroups []string
}

func New(logger *logger.Logger, targetUser string, targetGroups []string) *KubeDaemonPlugin {
	return &KubeDaemonPlugin{
		logger:       logger,
		doneChan:     make(chan struct{}),
		outputQueue:  make(chan plugin.ActionWrapper, 25),
		targetUser:   targetUser,
		targetGroups: targetGroups,
	}
}

func (k *KubeDaemonPlugin) Kill() {
	if k.action != nil {
		k.action.Kill()
	}
	k.killed = true
}

func (k *KubeDaemonPlugin) Done() <-chan struct{} {
	return k.doneChan
}

func (k *KubeDaemonPlugin) Outbox() <-chan plugin.ActionWrapper {
	return k.outputQueue
}

func (k *KubeDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	if k.action != nil {
		k.action.ReceiveStream(smessage)
	} else {
		k.logger.Debugf("db plugin received a stream message before an action was created. Ignoring")
	}
}

func (k *KubeDaemonPlugin) StartAction(action bzkube.KubeAction, logId string, command string, writer http.ResponseWriter, reader *http.Request) error {
	if k.killed {
		return fmt.Errorf("plugin has already been killed, cannot create a new kube action")
	}
	// Always generate a requestId, each new kube command is its own request
	// TODO: deprecated
	requestId := uuid.New().String()

	// Create action logger
	actLogger := k.logger.GetActionLogger(string(action))
	actLogger.AddRequestId(requestId)

	switch action {
	case bzkube.Exec:
		k.action = exec.New(actLogger, k.outputQueue, k.doneChan, requestId, logId, command)
	case bzkube.Stream:
		k.action = stream.New(actLogger, k.outputQueue, k.doneChan, requestId, logId, command)
	case bzkube.RestApi:
		k.action = restapi.New(actLogger, k.outputQueue, k.doneChan, requestId, logId, command)
	case bzkube.PortForward:
		k.action = portforward.New(actLogger, k.outputQueue, k.doneChan, requestId, logId, command)
	default:
		rerr := fmt.Errorf("unrecognized kubectl action: %s", action)
		k.logger.Error(rerr)
		return rerr
	}

	k.logger.Infof("Created %s action with url: %s", action, reader.URL.Path)

	// send http handlers to action
	if err := k.action.Start(writer, reader); err != nil {
		k.logger.Error(fmt.Errorf("%s error: %s", action, err))
	}
	return nil
}

func (k *KubeDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) error {
	// if actionPayload is empty, then there's nothing we need to process
	if len(actionPayload) == 0 {
		return nil
	} else if k.action == nil {
		return fmt.Errorf("received keysplitting message before action was created")
	}

	k.action.ReceiveKeysplitting(actionPayload)
	return nil
}
