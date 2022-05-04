package kube

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	kuberest "k8s.io/client-go/rest"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/kube/actions/exec"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/kube/actions/portforward"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/kube/actions/restapi"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/kube/actions/stream"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzkube "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type IKubeAction interface {
	Receive(action string, actionPayload []byte) ([]byte, error)
	Kill()
}

type KubePlugin struct {
	logger *logger.Logger

	doneChan         chan struct{}
	streamOutputChan chan smsg.StreamMessage
	action           IKubeAction

	serviceAccountToken string
	kubeHost            string
	targetUser          string
	targetGroups        []string
}

func New(
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	action string,
	payload []byte,
) (*KubePlugin, error) {

	// Unmarshal the Syn payload
	var synPayload bzkube.KubeActionParams
	if err := json.Unmarshal(payload, &synPayload); err != nil {
		return nil, fmt.Errorf("malformed Kube plugin SYN payload %v", string(payload))
	}

	// First load in our Kube variables
	config, err := kuberest.InClusterConfig()
	if err != nil {
		cerr := fmt.Errorf("error getting incluser config: %s", err)
		logger.Error(cerr)
		return nil, cerr
	}

	serviceAccountToken := config.BearerToken
	kubeHost := "https://" + os.Getenv("KUBERNETES_SERVICE_HOST")

	plugin := &KubePlugin{
		logger:              logger,
		doneChan:            make(chan struct{}),
		streamOutputChan:    ch,
		serviceAccountToken: serviceAccountToken,
		kubeHost:            kubeHost,
		targetUser:          synPayload.TargetUser,
		targetGroups:        synPayload.TargetGroups,
	}

	// Start up the action for this plugin
	subLogger := plugin.logger.GetActionLogger(action)
	if parsedAction, err := parseAction(action); err != nil {
		return nil, err
	} else {
		switch parsedAction {
		case bzkube.Exec:
			plugin.action = exec.New(subLogger, ch, plugin.doneChan, serviceAccountToken, kubeHost, synPayload.TargetGroups, synPayload.TargetUser)
		case bzkube.PortForward:
			plugin.action = portforward.New(subLogger, ch, plugin.doneChan, serviceAccountToken, kubeHost, synPayload.TargetGroups, synPayload.TargetUser)
		case bzkube.RestApi:
			plugin.action = restapi.New(subLogger, plugin.doneChan, serviceAccountToken, kubeHost, synPayload.TargetGroups, synPayload.TargetUser)
		case bzkube.Stream:
			plugin.action = stream.New(subLogger, ch, plugin.doneChan, serviceAccountToken, kubeHost, synPayload.TargetGroups, synPayload.TargetUser)
		default:
			return nil, fmt.Errorf("unhandled Kube action")
		}

		plugin.logger.Infof("Kube plugin started with %v action", action)
		return plugin, nil
	}
}

func (k *KubePlugin) Done() <-chan struct{} {
	return k.doneChan
}

func (k *KubePlugin) Kill() {
	if k.action != nil {
		k.action.Kill()
	}
}

func (k *KubePlugin) Receive(action string, actionPayload []byte) ([]byte, error) {
	k.logger.Debugf("Kube plugin received message with %s action", action)

	if payload, err := k.action.Receive(action, actionPayload); err != nil {
		return []byte{}, err
	} else {
		return payload, err
	}
}

func parseAction(action string) (bzkube.KubeAction, error) {
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", fmt.Errorf("malformed action: %s", action)
	}
	return bzkube.KubeAction(parsedAction[1]), nil
}
