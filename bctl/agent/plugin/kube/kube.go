package kube

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/kube/actions/exec"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/kube/actions/portforward"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/kube/actions/restapi"
	"bastionzero.com/bctl/v1/bctl/agent/plugin/kube/actions/stream"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzkube "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"

	kuberest "k8s.io/client-go/rest"
)

type IKubeAction interface {
	Receive(action string, actionPayload []byte) (string, []byte, error)
	Closed() bool
}

type KubePlugin struct {
	tmb *tomb.Tomb // datachannel's tomb

	logger *logger.Logger

	streamOutputChan chan smsg.StreamMessage
	action           IKubeAction

	serviceAccountToken string
	kubeHost            string
	targetUser          string
	targetGroups        []string
}

func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	action string,
	payload []byte) (*KubePlugin, error) {

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
		targetUser:          synPayload.TargetUser,
		targetGroups:        synPayload.TargetGroups,
		logger:              logger,
		tmb:                 parentTmb, // if datachannel dies, so should we
		streamOutputChan:    ch,
		serviceAccountToken: serviceAccountToken,
		kubeHost:            kubeHost,
	}

	// Start up the action for this plugin
	subLogger := plugin.logger.GetActionLogger(action)
	if parsedAction, err := parseAction(action); err != nil {
		return nil, err
	} else {
		var rerr error

		switch parsedAction {
		case bzkube.Exec:
			plugin.action, rerr = exec.New(subLogger, parentTmb, serviceAccountToken, kubeHost, synPayload.TargetGroups, synPayload.TargetUser, ch)
		case bzkube.PortForward:
			plugin.action, rerr = portforward.New(subLogger, parentTmb, serviceAccountToken, kubeHost, synPayload.TargetGroups, synPayload.TargetUser, ch)
		case bzkube.RestApi:
			plugin.action, rerr = restapi.New(subLogger, serviceAccountToken, kubeHost, synPayload.TargetGroups, synPayload.TargetUser)
		case bzkube.Stream:
			plugin.action, rerr = stream.New(subLogger, parentTmb, serviceAccountToken, kubeHost, synPayload.TargetGroups, synPayload.TargetUser, ch)
		default:
			rerr = fmt.Errorf("unhandled Kube action")
		}

		if rerr != nil {
			return nil, fmt.Errorf("failed to start Kube plugin with action %s: %s", action, rerr)
		} else {
			plugin.logger.Infof("Kube plugin started with %v action", action)
			return plugin, nil
		}
	}
}

func parseAction(action string) (bzkube.KubeAction, error) {
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", fmt.Errorf("malformed action: %s", action)
	}
	return bzkube.KubeAction(parsedAction[1]), nil
}

func (k *KubePlugin) Receive(action string, actionPayload []byte) (string, []byte, error) {
	k.logger.Debugf("Kube plugin received message with %s action", action)

	if safePayload, err := cleanPayload(actionPayload); err != nil {
		k.logger.Error(err)
		return "", []byte{}, err
	} else if action, payload, err := k.action.Receive(action, safePayload); err != nil {
		return "", []byte{}, err
	} else {
		return action, payload, err
	}
}

func cleanPayload(payload []byte) ([]byte, error) {
	// TODO: The below line removes the extra, surrounding quotation marks that get added at some point in the marshal/unmarshal
	// so it messes up the umarshalling into a valid action payload.  We need to figure out why this is happening
	// so that we can murder its family
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
