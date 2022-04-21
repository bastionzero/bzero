package portforward

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"gopkg.in/tomb.v2"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport/spdy"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	kubeaction "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// wrap this code so at test-time we can mock the dial / config
var doDial = func(dialer httpstream.Dialer, protocolName string) (httpstream.Connection, string, error) {
	return dialer.Dial(kubeutils.PortForwardProtocolV1Name)
}

var getConfig = func() (*rest.Config, error) {
	return rest.InClusterConfig()
}

type PortForwardAction struct {
	tmb    *tomb.Tomb
	logger *logger.Logger

	serviceAccountToken string
	kubeHost            string
	targetGroups        []string
	targetUser          string
	logId               string
	requestId           string
	closed              bool

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	// Done channel
	doneChan chan bool

	// Map of portforardId <-> PortForwardSubAction
	requestMap     map[string]*PortForwardRequest
	requestMapLock sync.Mutex

	// So we can recreate the port forward
	Endpoint        string
	DataHeaders     map[string]string
	ErrorHeaders    map[string]string
	CommandBeingRun string
	streamCh        httpstream.Connection
}

func New(logger *logger.Logger,
	pluginTmb *tomb.Tomb,
	serviceAccountToken string,
	kubeHost string,
	targetGroups []string,
	targetUser string,
	ch chan smsg.StreamMessage) (*PortForwardAction, error) {

	return &PortForwardAction{
		logger:              logger,
		tmb:                 pluginTmb,
		serviceAccountToken: serviceAccountToken,
		kubeHost:            kubeHost,
		targetGroups:        targetGroups,
		targetUser:          targetUser,
		closed:              false,
		streamOutputChan:    ch,
		requestMap:          make(map[string]*PortForwardRequest),
		doneChan:            make(chan bool),
	}, nil
}

func (p *PortForwardAction) Closed() bool {
	return p.closed
}

func (p *PortForwardAction) Receive(action string, actionPayload []byte) (string, []byte, error) {
	p.logger.Infof("PortForward Plugin received message with action: %s", action)
	switch portforward.PortForwardSubAction(action) {

	// Start portforward message required before anything else
	case portforward.StartPortForward:
		var startPortForwardRequest portforward.KubePortForwardStartActionPayload
		if err := json.Unmarshal(actionPayload, &startPortForwardRequest); err != nil {
			rerr := fmt.Errorf("unable to unmarshal start portforward message: %s", err)
			p.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		return p.startPortForward(startPortForwardRequest)
	case portforward.DataInPortForward, portforward.ErrorInPortForward:
		var dataInputAction portforward.KubePortForwardActionPayload
		if err := json.Unmarshal(actionPayload, &dataInputAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling datain: %s", err)
			p.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		if err := kubeutils.MatchRequestId(dataInputAction.RequestId, p.requestId); err != nil {
			p.logger.Error(err)
			return "", []byte{}, err
		}

		// See if we already have a session for this portforwardRequestId, else create it
		if oldRequest, ok := p.getRequestMap(dataInputAction.PortForwardRequestId); ok {
			oldRequest.portforwardDataInChannel <- dataInputAction.Data
		} else {
			// Create a new action and update our map
			subLogger := p.logger.GetActionLogger("kube/portforward/agent/request")
			subLogger.AddRequestId(p.requestId)
			newRequest := &PortForwardRequest{
				logger:                    subLogger,
				streamOutputChan:          p.streamOutputChan,
				streamMessageVersion:      p.streamMessageVersion,
				portforwardDataInChannel:  make(chan []byte),
				portforwardErrorInChannel: make(chan []byte),
				tmb:                       p.tmb,
				doneChan:                  make(chan bool),
			}
			if err := newRequest.openPortForwardStream(dataInputAction.PortForwardRequestId, p.DataHeaders, p.ErrorHeaders, p.targetUser, p.logId, p.requestId, p.Endpoint, dataInputAction.PodPort, p.targetGroups, p.streamCh); err != nil {
				rerr := fmt.Errorf("error opening stream for new portforward request: %s", err)
				p.logger.Error(rerr)
				return "", []byte{}, rerr
			}
			p.updateRequestMap(newRequest, dataInputAction.PortForwardRequestId)
			newRequest.portforwardDataInChannel <- dataInputAction.Data
		}

		return string(action), []byte{}, nil
	case portforward.StopPortForwardRequest:
		var stopRequestAction portforward.KubePortForwardStopRequestActionPayload
		if err := json.Unmarshal(actionPayload, &stopRequestAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling stop request: %s", err)
			p.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		// If we haven't recvied a start message, just leave
		if err := kubeutils.MatchRequestId(stopRequestAction.RequestId, p.requestId); err != nil {
			p.logger.Error(err)
			return string(portforward.StopPortForwardRequest), []byte{}, nil
		}

		// Alert on the done channel
		if portForwardRequest, ok := p.getRequestMap(stopRequestAction.PortForwardRequestId); ok {
			portForwardRequest.doneChan <- true
		}

		// Else update our requestMap
		p.deleteRequestMap(stopRequestAction.PortForwardRequestId)

		return string(portforward.StopPortForwardRequest), []byte{}, nil
	case portforward.StopPortForward:
		// We decrypt the message, incase no start message was sent over the port forward session
		var stopAction portforward.KubePortForwardStopActionPayload
		if err := json.Unmarshal(actionPayload, &stopAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling stop request: %s", err)
			p.logger.Error(rerr)
			return "", []byte{}, rerr
		}
		p.logger.Infof("Stopping port forward action for requestId: %s", p.requestId)

		if err := kubeutils.MatchRequestId(stopAction.RequestId, p.requestId); err != nil {
			p.logger.Error(err)
			return string(portforward.StopPortForward), []byte{}, nil
		}

		// Alert on our done channel
		p.doneChan <- true

		// Stop the streamch
		if p.streamCh != nil {
			p.streamCh.Close()
		}

		// Set ourselves to closed so this object will get dereferenced
		p.closed = true

		return string(portforward.StopPortForward), []byte{}, nil
	default:
		rerr := fmt.Errorf("unhandled portforward action: %v", action)
		p.logger.Error(rerr)
		return "", []byte{}, rerr
	}
}

func (p *PortForwardAction) startPortForward(startPortForwardRequest portforward.KubePortForwardStartActionPayload) (string, []byte, error) {
	// Update our object to keep track of the pod and url information
	p.DataHeaders = startPortForwardRequest.DataHeaders
	p.ErrorHeaders = startPortForwardRequest.ErrorHeaders
	p.Endpoint = startPortForwardRequest.Endpoint
	p.logId = startPortForwardRequest.LogId
	p.doneChan = make(chan bool, 1)

	// keep track of who we're talking to
	p.requestId = startPortForwardRequest.RequestId
	p.logger.Infof("Setting request id: %s", p.requestId)
	p.streamMessageVersion = startPortForwardRequest.StreamMessageVersion
	p.logger.Infof("Setting stream message version: %s", p.streamMessageVersion)

	// Now make our stream chan
	// Create the in-cluster config
	config, err := getConfig()
	if err != nil {
		rerr := fmt.Errorf("error creating in-custer config: %s", err)
		p.logger.Error(rerr)
		return "", []byte{}, err
	}

	// Always ensure that our targetUser is set
	if p.targetUser == "" {
		rerr := fmt.Errorf("target user field is not set")
		p.logger.Error(rerr)
		return "", []byte{}, err
	}

	// Add our impersonation information
	config.Impersonate = rest.ImpersonationConfig{
		UserName: p.targetUser,
		Groups:   p.targetGroups,
	}
	config.BearerToken = p.serviceAccountToken

	// Start building our spdy stream
	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		rerr := fmt.Errorf("error creating spdy RoundTripper: %s", err)
		p.logger.Error(rerr)
		return "", []byte{}, err
	}

	hostIP := strings.TrimLeft(config.Host, "htps:/")
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, &url.URL{Scheme: "https", Path: p.Endpoint, Host: hostIP})

	var readyMessageErr string
	streamCh, protocolSelected, err := doDial(dialer, kubeutils.PortForwardProtocolV1Name)
	if err != nil {
		rerr := fmt.Errorf("error dialing portforward spdy stream: %s", err)
		p.logger.Error(rerr)
		readyMessageErr = err.Error()
	} else {
		p.logger.Infof("Dial successful. Selected protocol: %s", protocolSelected)
	}

	switch p.streamMessageVersion {
	// prior to 202204
	case "":
		p.sendReadyMessage(smsg.ReadyPortForward, readyMessageErr)
	default:
		p.sendReadyMessage(smsg.Ready, readyMessageErr)
	}

	// Save the streamCh to use later
	p.streamCh = streamCh

	return string(portforward.StartPortForward), []byte{}, nil
}

func (p *PortForwardAction) sendReadyMessage(streamType smsg.StreamType, errorMessage string) {
	p.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion:  p.streamMessageVersion,
		SequenceNumber: 0,
		RequestId:      p.requestId,
		LogId:          p.logId,
		Action:         string(kubeaction.PortForward),
		Type:           streamType,
		Content:        errorMessage,
	}
}

// Helper function so we avoid writing to this map at the same time
func (p *PortForwardAction) updateRequestMap(newPortForwardRequest *PortForwardRequest, key string) {
	p.requestMapLock.Lock()
	p.requestMap[key] = newPortForwardRequest
	p.requestMapLock.Unlock()
}

func (p *PortForwardAction) deleteRequestMap(key string) {
	p.requestMapLock.Lock()
	delete(p.requestMap, key)
	p.requestMapLock.Unlock()
}

func (p *PortForwardAction) getRequestMap(key string) (*PortForwardRequest, bool) {
	p.requestMapLock.Lock()
	defer p.requestMapLock.Unlock()
	act, ok := p.requestMap[key]
	return act, ok
}
