package portforward

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"gopkg.in/tomb.v2"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport/spdy"

	kubeutils "bastionzero.com/bctl/v1/bctl/agent/plugin/kube/utils"
	kubeutilsdaemon "bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/utils"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	kubeaction "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
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

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan chan smsg.StreamMessage

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

func (a *PortForwardAction) Closed() bool {
	return a.closed
}

func (a *PortForwardAction) Receive(action string, actionPayload []byte) (string, []byte, error) {
	a.logger.Infof("PortForward Plugin received message with action: %s", action)
	switch portforward.PortForwardSubAction(action) {

	// Start portforward message required before anything else
	case portforward.StartPortForward:
		var startPortForwardRequest portforward.KubePortForwardStartActionPayload
		if err := json.Unmarshal(actionPayload, &startPortForwardRequest); err != nil {
			rerr := fmt.Errorf("unable to unmarshal start portforward message: %s", err)
			a.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		return a.startPortForward(startPortForwardRequest)
	case portforward.DataInPortForward, portforward.ErrorInPortForward:
		var dataInputAction portforward.KubePortForwardActionPayload
		if err := json.Unmarshal(actionPayload, &dataInputAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling datain: %s", err)
			a.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		if err := kubeutils.MatchRequestId(dataInputAction.RequestId, a.requestId); err != nil {
			a.logger.Error(err)
			return "", []byte{}, err
		}

		// See if we already have a session for this portforwardRequestId, else create it
		if oldRequest, ok := a.getRequestMap(dataInputAction.PortForwardRequestId); ok {
			oldRequest.portforwardDataInChannel <- dataInputAction.Data
		} else {
			// Create a new action and update our map
			subLogger := a.logger.GetActionLogger("kube/portforward/agent/request")
			subLogger.AddRequestId(a.requestId)
			newRequest := &PortForwardRequest{
				logger:                    subLogger,
				streamOutputChan:          a.streamOutputChan,
				portforwardDataInChannel:  make(chan []byte),
				portforwardErrorInChannel: make(chan []byte),
				tmb:                       a.tmb,
				doneChan:                  make(chan bool),
			}
			if err := newRequest.openPortForwardStream(dataInputAction.PortForwardRequestId, a.DataHeaders, a.ErrorHeaders, a.targetUser, a.logId, a.requestId, a.Endpoint, dataInputAction.PodPort, a.targetGroups, a.streamCh); err != nil {
				rerr := fmt.Errorf("error opening stream for new portforward request: %s", err)
				a.logger.Error(rerr)
				return "", []byte{}, rerr
			}
			a.updateRequestMap(newRequest, dataInputAction.PortForwardRequestId)
			newRequest.portforwardDataInChannel <- dataInputAction.Data
		}

		return string(action), []byte{}, nil
	case portforward.StopPortForwardRequest:
		var stopRequestAction portforward.KubePortForwardStopRequestActionPayload
		if err := json.Unmarshal(actionPayload, &stopRequestAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling stop request: %s", err)
			a.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		// If we haven't recvied a start message, just leave
		if err := kubeutils.MatchRequestId(stopRequestAction.RequestId, a.requestId); err != nil {
			a.logger.Error(err)
			return string(portforward.StopPortForwardRequest), []byte{}, nil
		}

		// Alert on the done channel
		if portForwardRequest, ok := a.getRequestMap(stopRequestAction.PortForwardRequestId); ok {
			portForwardRequest.doneChan <- true
		}

		// Else update our requestMap
		a.deleteRequestMap(stopRequestAction.PortForwardRequestId)

		return string(portforward.StopPortForwardRequest), []byte{}, nil
	case portforward.StopPortForward:
		// We decrypt the message, incase no start message was sent over the port forward session
		var stopAction portforward.KubePortForwardStopActionPayload
		if err := json.Unmarshal(actionPayload, &stopAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling stop request: %s", err)
			a.logger.Error(rerr)
			return "", []byte{}, rerr
		}
		a.logger.Infof("Stopping port forward action for requestId: %s", a.requestId)

		if err := kubeutils.MatchRequestId(stopAction.RequestId, a.requestId); err != nil {
			a.logger.Error(err)
			return string(portforward.StopPortForward), []byte{}, nil
		}

		// Alert on our done channel
		a.doneChan <- true

		// Stop the streamch
		if a.streamCh != nil {
			a.streamCh.Close()
		}

		// Set ourselves to closed so this object will get dereferenced
		a.closed = true

		return string(portforward.StopPortForward), []byte{}, nil
	default:
		rerr := fmt.Errorf("unhandled portforward action: %v", action)
		a.logger.Error(rerr)
		return "", []byte{}, rerr
	}
}

func (a *PortForwardAction) startPortForward(startPortForwardRequest portforward.KubePortForwardStartActionPayload) (string, []byte, error) {
	// Update our object to keep track of the pod and url information
	a.DataHeaders = startPortForwardRequest.DataHeaders
	a.ErrorHeaders = startPortForwardRequest.ErrorHeaders
	a.Endpoint = startPortForwardRequest.Endpoint
	a.logId = startPortForwardRequest.LogId
	a.requestId = startPortForwardRequest.RequestId
	a.doneChan = make(chan bool, 1)

	// Now make our stream chan
	// Create the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		rerr := fmt.Errorf("error creating in-custer config: %s", err)
		a.logger.Error(rerr)
		return "", []byte{}, err
	}

	// Always ensure that our targetUser is set
	if a.targetUser == "" {
		rerr := fmt.Errorf("target user field is not set")
		a.logger.Error(rerr)
		return "", []byte{}, err
	}

	// Add our impersonation information
	config.Impersonate = rest.ImpersonationConfig{
		UserName: a.targetUser,
		Groups:   a.targetGroups,
	}
	config.BearerToken = a.serviceAccountToken

	// Start building our spdy stream
	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		rerr := fmt.Errorf("error creating spdy RoundTripper: %s", err)
		a.logger.Error(rerr)
		return "", []byte{}, err
	}

	hostIP := strings.TrimLeft(config.Host, "htps:/")
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, &url.URL{Scheme: "https", Path: a.Endpoint, Host: hostIP})
	streamCh, protocolSelected, err := dialer.Dial(kubeutilsdaemon.PortForwardProtocolV1Name)
	if err != nil {
		rerr := fmt.Errorf("error dialing portforward spdy stream: %s", err)
		a.logger.Error(rerr)

		// Let the user know about this error
		a.sendReadyMessage(err.Error())
	} else {
		a.logger.Infof("Dial successful. Selected protocol: %s", protocolSelected)

		// Let the user know we are ready
		a.sendReadyMessage("")
	}

	// Save the streamCh to use later
	a.streamCh = streamCh

	return string(portforward.StartPortForward), []byte{}, nil
}

func (a *PortForwardAction) sendReadyMessage(errorMessage string) {
	message := smsg.StreamMessage{
		SchemaVersion:  smsg.CurrentSchema,
		SequenceNumber: 0,
		RequestId:      a.requestId,
		Action:         string(kubeaction.PortForward),
		Type:           smsg.ReadyPortForward,
		TypeV2:         smsg.Ready,
		Content:        errorMessage,
	}
	a.streamOutputChan <- message
}

// Helper function so we avoid writing to this map at the same time
func (a *PortForwardAction) updateRequestMap(newPortForwardRequest *PortForwardRequest, key string) {
	a.requestMapLock.Lock()
	a.requestMap[key] = newPortForwardRequest
	a.requestMapLock.Unlock()
}

func (a *PortForwardAction) deleteRequestMap(key string) {
	a.requestMapLock.Lock()
	delete(a.requestMap, key)
	a.requestMapLock.Unlock()
}

func (a *PortForwardAction) getRequestMap(key string) (*PortForwardRequest, bool) {
	a.requestMapLock.Lock()
	defer a.requestMapLock.Unlock()
	act, ok := a.requestMap[key]
	return act, ok
}

type PortForwardRequest struct {
	tmb    *tomb.Tomb
	logger *logger.Logger

	// To send data/error to our portforward sessions
	portforwardDataInChannel  chan []byte
	portforwardErrorInChannel chan []byte

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan chan smsg.StreamMessage

	// Done channel so the go routines can communicate with eachother
	doneChan chan bool
}

func (r *PortForwardRequest) openPortForwardStream(portforwardRequestId string, dataHeaders map[string]string, errorHeaders map[string]string, targetUser, logId, requestId, endpoint string, podPort int64, targetGroups []string, streamCh httpstream.Connection) error {
	r.logger.Infof("Starting port forward connection for: %s on port: %d. PortforwardRequestId: %ss", endpoint, podPort, portforwardRequestId)

	// Update our error headers to include the podPort
	errorHeaders[kubeutilsdaemon.PortHeader] = fmt.Sprintf("%d", podPort)
	errorHeaders[kubeutilsdaemon.PortForwardRequestIDHeader] = portforwardRequestId

	// Create our two streams with the provided headers
	// We purposely share the header object for data and error stream
	headers := http.Header{}
	for name, value := range errorHeaders {
		headers.Add(name, value)
	}
	// Create our http.Header
	errorStream, err := streamCh.CreateStream(headers)
	if err != nil {
		rerr := fmt.Errorf("error creating error stream: %s", err)
		r.logger.Error(rerr)
		return rerr
	}

	for name, value := range dataHeaders {
		// Set so we override any error headers that were set
		headers.Set(name, value)
	}
	// Create our http.Header
	dataStream, err := streamCh.CreateStream(headers)
	if err != nil {
		rerr := fmt.Errorf("error creating data stream: %s", err)
		r.logger.Error(rerr)
		return rerr
	}

	// We need to set up two go routines for our data/error-in channel (i.e. coming from the user)
	go func() {
		for {
			select {
			case <-r.tmb.Dying():
				return
			case dataInMessage := <-r.portforwardDataInChannel:
				// Make this request locally, and then return that info to the user
				if _, err := io.Copy(dataStream, bytes.NewReader(dataInMessage)); err != nil {
					r.logger.Error(fmt.Errorf("error writing to data stream: %s", err))
					r.doneChan <- true
					dataStream.Close()
					return
				}
			}
		}
	}()

	// For our error-in
	go func() {
		for {
			select {
			case <-r.tmb.Dying():
				return
			case errorInMessage := <-r.portforwardErrorInChannel:
				// Make this request locally, and then return that info to the user
				if _, err := io.Copy(errorStream, bytes.NewReader(errorInMessage)); err != nil {
					r.logger.Error(fmt.Errorf("error writing to error stream: %s", err))

					// Do not alert on anything
					return
				}
			}
		}
	}()

	// Set up a go routine to listen for to our dataStream and send to the client
	go func() {
		defer dataStream.Close()

		// Keep track of seq number
		dataSeqNumber := 0

		for {
			select {
			case <-r.tmb.Dying():
				return
			default:
				r.forwardStream(smsg.Data, dataStream, dataSeqNumber, portforwardRequestId, requestId, logId)
				dataSeqNumber += 1
			}
		}
	}()

	// Setup a go routine for the error stream as well
	go func() {
		defer errorStream.Close()

		// Keep track of seq number
		errorSeqNumber := 0

		for {
			select {
			case <-r.tmb.Dying():
				return
			default:
				r.forwardStream(smsg.Error, errorStream, errorSeqNumber, portforwardRequestId, requestId, logId)
				errorSeqNumber += 1
			}
		}
	}()

	// If we get a message on the done channel, set our bool to closed
	go func() {
		defer errorStream.Close()
		defer dataStream.Close()
		for {
			select {
			case <-r.tmb.Dying():
				return
			case <-r.doneChan:
				return
			}
		}
	}()

	return nil
}

func (r *PortForwardRequest) forwardStream(
	streamType smsg.StreamType,
	stream httpstream.Stream,
	sequenceNumber int,
	portforwardRequestId string,
	requestId string,
	logId string,
) {
	buf := make([]byte, portforward.DataStreamBufferSize)
	n, err := stream.Read(buf)
	if err != nil {
		if err != io.EOF {
			rerr := fmt.Errorf("error reading data from data stream: %s", err)
			r.logger.Error(rerr)
		} else if streamType == smsg.Data {
			content, err := r.wrapStreamMessageContent([]byte{}, portforwardRequestId)
			if err != nil {
				r.logger.Error(err)

				// Alert on our done channel
				r.doneChan <- true
			}

			// Send our end message
			message := smsg.StreamMessage{
				SchemaVersion:  smsg.CurrentSchema,
				Action:         string(kubeaction.PortForward),
				Type:           streamType,
				RequestId:      requestId,
				LogId:          logId,
				SequenceNumber: sequenceNumber,
				Content:        content,
				More:           false,
			}
			r.streamOutputChan <- message
		}
		r.doneChan <- true
		return
	}

	// Send this data back to the bastion
	content, err := r.wrapStreamMessageContent(buf[:n], portforwardRequestId)
	if err != nil {
		r.logger.Error(err)

		// Alert on our done channel
		r.doneChan <- true
	}

	message := smsg.StreamMessage{
		SchemaVersion:  smsg.CurrentSchema,
		SequenceNumber: sequenceNumber,
		RequestId:      requestId,
		Action:         string(kubeaction.PortForward),
		Type:           streamType,
		More:           true,
		Content:        content,
		LogId:          logId,
	}
	r.streamOutputChan <- message
}

func (r *PortForwardRequest) wrapStreamMessageContent(content []byte, portforwardRequestId string) (string, error) {
	streamMessageToSend := portforward.KubePortForwardStreamMessageContent{
		PortForwardRequestId: portforwardRequestId,
		Content:              content,
	}
	streamMessageToSendBytes, err := json.Marshal(streamMessageToSend)
	if err != nil {
		rerr := fmt.Errorf("error marsheling stream message: %s", err)

		return "", rerr
	}

	return base64.StdEncoding.EncodeToString(streamMessageToSendBytes), nil
}
