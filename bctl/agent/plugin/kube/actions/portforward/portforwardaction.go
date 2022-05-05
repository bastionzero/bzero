package portforward

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
	logger *logger.Logger

	serviceAccountToken string
	kubeHost            string
	targetGroups        []string
	targetUser          string
	logId               string
	requestId           string

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	// Done channel
	doneChan chan struct{}

	// Map of portforardId <-> PortForwardSubAction
	requestMap map[string]*PortForwardRequest

	// So we can recreate the port forward
	Endpoint        string
	DataHeaders     map[string]string
	ErrorHeaders    map[string]string
	CommandBeingRun string
	streamConn      httpstream.Connection
}

func New(
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	doneChan chan struct{},
	serviceAccountToken string,
	kubeHost string,
	targetGroups []string,
	targetUser string,
) *PortForwardAction {

	return &PortForwardAction{
		logger:              logger,
		serviceAccountToken: serviceAccountToken,
		kubeHost:            kubeHost,
		targetGroups:        targetGroups,
		targetUser:          targetUser,
		streamOutputChan:    ch,
		requestMap:          make(map[string]*PortForwardRequest),
		doneChan:            doneChan,
	}
}

func (p *PortForwardAction) Kill() {
	for _, val := range p.requestMap {
		val.Kill()
	}

	// close the connection
	if p.streamConn != nil {
		p.streamConn.Close()
	}
}

func (p *PortForwardAction) Receive(action string, actionPayload []byte) ([]byte, error) {
	p.logger.Infof("PortForward action received message with action: %s", action)
	switch portforward.PortForwardSubAction(action) {

	// Start portforward message required before anything else
	case portforward.StartPortForward:
		var startPortForwardRequest portforward.KubePortForwardStartActionPayload
		if err := json.Unmarshal(actionPayload, &startPortForwardRequest); err != nil {
			rerr := fmt.Errorf("unable to unmarshal start portforward message: %s", string(actionPayload))
			p.logger.Error(rerr)
			return []byte{}, rerr
		}

		return p.startPortForward(startPortForwardRequest)
	case portforward.DataInPortForward, portforward.ErrorInPortForward:
		var dataInputAction portforward.KubePortForwardActionPayload
		if err := json.Unmarshal(actionPayload, &dataInputAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling datain: %s", err)
			p.logger.Error(rerr)
			return []byte{}, rerr
		}

		// See if we already have a session for this portforwardRequestId, else create it
		if oldRequest, ok := p.requestMap[dataInputAction.PortForwardRequestId]; ok {
			oldRequest.dataInChannel <- dataInputAction.Data
		} else {
			// Create a new action and update our map
			subLogger := p.logger.GetActionLogger("kube/portforward/agent/request")
			subLogger.AddRequestId(p.requestId)
			newRequest := createPortForwardRequest(
				subLogger,
				p.streamOutputChan,
				p.streamMessageVersion,
				p.requestId,
				p.logId,
				dataInputAction.PortForwardRequestId,
			)

			p.logger.Infof("Starting port forwarding for %s on port %d. PortforwardRequestId: %s", p.Endpoint, dataInputAction.PodPort, dataInputAction.PortForwardRequestId)
			if err := newRequest.openPortForwardStream(p.DataHeaders, p.ErrorHeaders, dataInputAction.PodPort, p.streamConn); err != nil {
				rerr := fmt.Errorf("error opening stream for new portforward request: %s", err)
				p.logger.Error(rerr)
				return []byte{}, rerr
			}
			p.requestMap[dataInputAction.PortForwardRequestId] = newRequest
			newRequest.dataInChannel <- dataInputAction.Data
		}

		return []byte{}, nil
	case portforward.StopPortForwardRequest:
		var stopRequestAction portforward.KubePortForwardStopRequestActionPayload
		if err := json.Unmarshal(actionPayload, &stopRequestAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling stop request: %s", err)
			p.logger.Error(rerr)
			return []byte{}, rerr
		}

		// Alert on the done channel
		if portForwardRequest, ok := p.requestMap[stopRequestAction.PortForwardRequestId]; ok {
			portForwardRequest.Kill()
		}

		// Else update our requestMap
		delete(p.requestMap, stopRequestAction.PortForwardRequestId)

		return []byte{}, nil
	case portforward.StopPortForward:
		p.Kill()

		return []byte{}, nil
	default:
		rerr := fmt.Errorf("unhandled portforward action: %v", action)
		p.logger.Error(rerr)
		return []byte{}, rerr
	}
}

func (p *PortForwardAction) startPortForward(startPortForwardRequest portforward.KubePortForwardStartActionPayload) ([]byte, error) {
	// Update our object to keep track of the pod and url information
	p.DataHeaders = startPortForwardRequest.DataHeaders
	p.ErrorHeaders = startPortForwardRequest.ErrorHeaders
	p.Endpoint = startPortForwardRequest.Endpoint
	p.logId = startPortForwardRequest.LogId

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
		return []byte{}, rerr
	}

	// Always ensure that our targetUser is set
	if p.targetUser == "" {
		rerr := fmt.Errorf("target user field is not set")
		p.logger.Error(rerr)
		return []byte{}, rerr
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
		return []byte{}, rerr
	}

	hostIP := strings.TrimLeft(config.Host, "htps:/")
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, &url.URL{Scheme: "https", Path: p.Endpoint, Host: hostIP})

	var readyMessageErr string
	streamConn, protocolSelected, err := doDial(dialer, kubeutils.PortForwardProtocolV1Name)
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

	// Save the connection to use later
	p.streamConn = streamConn

	// track when the http stream connection has closed so we know when we're done
	go func() {
		<-streamConn.CloseChan()
		close(p.doneChan)
	}()

	return []byte{}, nil
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
