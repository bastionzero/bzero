package kube

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

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

type KubeAction string

const (
	Exec        KubeAction = "exec"
	RestApi     KubeAction = "restapi"
	Stream      KubeAction = "stream"
	PortForward KubeAction = "portforward"
)

type JustRequestId struct {
	RequestId string `json:"requestId"`
}

type KubePlugin struct {
	tmb *tomb.Tomb // datachannel's tomb

	logger         *logger.Logger
	actionsMapLock sync.Mutex

	streamOutputChan chan smsg.StreamMessage
	actions          map[string]IKubeAction

	serviceAccountToken string
	kubeHost            string
	targetUser          string
	targetGroups        []string
}

func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	payload []byte) (*KubePlugin, error) {

	// Unmarshal the Syn payload
	var synPayload bzkube.KubeActionParams
	if err := json.Unmarshal(payload, &synPayload); err != nil {
		return &KubePlugin{}, fmt.Errorf("malformed Kube plugin SYN payload %v", string(payload))
	}

	// First load in our Kube variables
	config, err := kuberest.InClusterConfig()
	if err != nil {
		cerr := fmt.Errorf("error getting incluser config: %s", err)
		logger.Error(cerr)
		return &KubePlugin{}, cerr
	}

	serviceAccountToken := config.BearerToken
	kubeHost := "https://" + os.Getenv("KUBERNETES_SERVICE_HOST")

	return &KubePlugin{
		targetUser:          synPayload.TargetUser,
		targetGroups:        synPayload.TargetGroups,
		logger:              logger,
		tmb:                 parentTmb, // if datachannel dies, so should we
		streamOutputChan:    ch,
		actions:             make(map[string]IKubeAction),
		serviceAccountToken: serviceAccountToken,
		kubeHost:            kubeHost,
	}, nil
}

func (k *KubePlugin) Receive(action string, actionPayload []byte) (string, []byte, error) {
	// Get the action so we know where to send the payload
	k.logger.Infof("Plugin received Data message with %v action", action)

	// parse action
	parsedAction := strings.Split(action, "/")
	if len(parsedAction) < 2 {
		return "", []byte{}, fmt.Errorf("malformed action: %s", action)
	}
	kubeAction := parsedAction[1]

	// TODO: The below line removes the extra, surrounding quotation marks that get added at some point in the marshal/unmarshal
	// so it messes up the umarshalling into a valid action payload.  We need to figure out why this is happening
	// so that we can murder its family
	if len(actionPayload) > 0 {
		actionPayload = actionPayload[1 : len(actionPayload)-1]
	}

	// Json unmarshalling encodes bytes in base64
	actionPayloadSafe, _ := base64.StdEncoding.DecodeString(string(actionPayload))

	// Grab just the request ID so that we can look up whether it's associated with a previously started action object
	var justrid JustRequestId
	var rid string
	if err := json.Unmarshal(actionPayloadSafe, &justrid); err != nil {
		return "", []byte{}, fmt.Errorf("could not unmarshal json: %v", err.Error())
	} else {
		rid = justrid.RequestId
	}

	// Interactive commands like exec and log need to be able to receive multiple inputs, so we start them and track them
	// and send any new messages with the same request ID to the existing action object
	if act, ok := k.getActionsMap(rid); ok {
		action, payload, err := act.Receive(action, actionPayloadSafe)

		// Check if that last message closed the action, if so delete from map
		if act.Closed() {
			k.deleteActionsMap(rid)
		}

		return action, payload, err
	} else {
		subLogger := k.logger.GetActionLogger(action)
		subLogger.AddRequestId(rid)
		// Create an action object if we don't already have one for the incoming request id
		var a IKubeAction
		var err error

		switch KubeAction(kubeAction) {
		case RestApi:
			a, err = restapi.New(subLogger, k.serviceAccountToken, k.kubeHost, k.targetGroups, k.targetUser)
		case Exec:
			a, err = exec.New(subLogger, k.tmb, k.serviceAccountToken, k.kubeHost, k.targetGroups, k.targetUser, k.streamOutputChan)
			k.updateActionsMap(a, rid) // save action for later input
		case Stream:
			a, err = stream.New(subLogger, k.tmb, k.serviceAccountToken, k.kubeHost, k.targetGroups, k.targetUser, k.streamOutputChan)
			k.updateActionsMap(a, rid) // save action for later input
		case PortForward:
			a, err = portforward.New(subLogger, k.tmb, k.serviceAccountToken, k.kubeHost, k.targetGroups, k.targetUser, k.streamOutputChan)
			k.updateActionsMap(a, rid) // save action for later input
		default:
			msg := fmt.Sprintf("unhandled kubeAction: %s", kubeAction)
			err = errors.New(msg)
		}

		if err != nil {
			rerr := fmt.Errorf("could not start new action object: %s", err)
			k.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		// Send the payload to the action and add it to the map for future incoming requests
		action, payload, err := a.Receive(action, actionPayloadSafe)
		return action, payload, err
	}
}

// Helper function so we avoid writing to this map at the same time
func (k *KubePlugin) updateActionsMap(newAction IKubeAction, id string) {
	k.actionsMapLock.Lock()
	k.actions[id] = newAction
	k.actionsMapLock.Unlock()
}

func (k *KubePlugin) deleteActionsMap(rid string) {
	k.actionsMapLock.Lock()
	delete(k.actions, rid)
	k.actionsMapLock.Unlock()
}

func (k *KubePlugin) getActionsMap(rid string) (IKubeAction, bool) {
	k.actionsMapLock.Lock()
	defer k.actionsMapLock.Unlock()
	act, ok := k.actions[rid]
	return act, ok
}
