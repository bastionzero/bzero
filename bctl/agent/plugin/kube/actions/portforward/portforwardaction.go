package portforward

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"gopkg.in/tomb.v2"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport/spdy"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzkube "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

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

	request *PortForwardRequest

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	// Done channel
	doneChan chan bool

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

		// See if we already have a session for this portforwardRequestId, else create it
		if p.request == nil {
			// Create a new action and update our map
			// FIXME: still need these two lines?
			subLogger := p.logger.GetActionLogger("kube/portforward/agent/request")
			subLogger.AddRequestId(p.requestId)
			p.request = createPortForwardRequest(
				p.tmb,
				p.logger,
				dataInputAction.PodPort,
				p.DataHeaders,
				p.ErrorHeaders,
				p.logId,
				p.requestId,
				dataInputAction.PortForwardRequestId,
				p.streamOutputChan,
				p.streamMessageVersion,
			)
			if err := p.request.openPortForwardStream(p.Endpoint, p.streamCh); err != nil {
				rerr := fmt.Errorf("error opening stream for new portforward request: %s", err)
				p.logger.Error(rerr)
				return "", []byte{}, rerr
			}
		}
		p.request.portforwardDataInChannel <- dataInputAction.Data
		return string(action), []byte{}, nil

	case portforward.StopPortForwardRequest:
		var stopRequestAction portforward.KubePortForwardStopRequestActionPayload
		if err := json.Unmarshal(actionPayload, &stopRequestAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling stop request: %s", err)
			p.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		// Alert on the done channel
		p.request.doneChan <- true

		p.request = nil
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
	config, err := rest.InClusterConfig()
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
	streamCh, protocolSelected, err := dialer.Dial(kubeutils.PortForwardProtocolV1Name)
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
		Action:         string(bzkube.PortForward),
		Type:           streamType,
		Content:        errorMessage,
	}
}
