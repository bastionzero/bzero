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
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type PortForwardSubAction string

const (
	StartPortForward       PortForwardSubAction = "kube/portforward/start"
	DataInPortForward      PortForwardSubAction = "kube/portforward/datain"
	ErrorInPortForward     PortForwardSubAction = "kube/portforward/errorin"
	ReadyPortForward       PortForwardSubAction = "kube/portforward/ready"
	DataPortForward        PortForwardSubAction = "kube/portforward/data"
	ErrorPortForward       PortForwardSubAction = "kube/portforward/error"
	StopPortForward        PortForwardSubAction = "kube/portforward/stop"
	StopPortForwardRequest PortForwardSubAction = "kube/portforward/request/stop"
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
	switch PortForwardSubAction(action) {

	// Start portforward message required before anything else
	case StartPortForward:
		var startPortForwardRequest KubePortForwardStartActionPayload
		if err := json.Unmarshal(actionPayload, &startPortForwardRequest); err != nil {
			rerr := fmt.Errorf("unable to unmarshal start portforward message: %s", err)
			p.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		return p.StartPortForward(startPortForwardRequest)
	case DataInPortForward, ErrorInPortForward:
		var dataInputAction KubePortForwardActionPayload
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
	case StopPortForwardRequest:
		var stopRequestAction KubePortForwardStopRequestActionPayload
		if err := json.Unmarshal(actionPayload, &stopRequestAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling stop request: %s", err)
			p.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		// If we haven't recvied a start message, just leave
		if err := kubeutils.MatchRequestId(stopRequestAction.RequestId, p.requestId); err != nil {
			p.logger.Error(err)
			return string(StopPortForwardRequest), []byte{}, nil
		}

		// Alert on the done channel
		if portForwardRequest, ok := p.getRequestMap(stopRequestAction.PortForwardRequestId); ok {
			portForwardRequest.doneChan <- true
		}

		// Else update our requestMap
		p.deleteRequestMap(stopRequestAction.PortForwardRequestId)

		return string(StopPortForwardRequest), []byte{}, nil
	case StopPortForward:
		// We decrypt the message, incase no start message was sent over the port forward session
		var stopAction KubePortForwardStopActionPayload
		if err := json.Unmarshal(actionPayload, &stopAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling stop request: %s", err)
			p.logger.Error(rerr)
			return "", []byte{}, rerr
		}
		p.logger.Infof("Stopping port forward action for requestId: %s", p.requestId)

		if err := kubeutils.MatchRequestId(stopAction.RequestId, p.requestId); err != nil {
			p.logger.Error(err)
			return string(StopPortForward), []byte{}, nil
		}

		// Alert on our done channel
		p.doneChan <- true

		// Stop the streamch
		if p.streamCh != nil {
			p.streamCh.Close()
		}

		// Set ourselves to closed so this object will get dereferenced
		p.closed = true

		return string(StopPortForward), []byte{}, nil
	default:
		rerr := fmt.Errorf("unhandled portforward action: %v", action)
		p.logger.Error(rerr)
		return "", []byte{}, rerr
	}
}

func (p *PortForwardRequest) openPortForwardStream(portforwardRequestId string, dataHeaders map[string]string, errorHeaders map[string]string, targetUser, logId, requestId, endpoint string, podPort int64, targetGroups []string, streamCh httpstream.Connection) error {
	p.logger.Infof("Starting port forward connection for: %s on port: %d. PortforwardRequestId: %ss", endpoint, podPort, portforwardRequestId)

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
		p.logger.Error(rerr)
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
		p.logger.Error(rerr)
		return rerr
	}

	// We need to set up two go routines for our data/error-in channel (i.e. coming from the user)
	go func() {
		for {
			select {
			case <-p.tmb.Dying():
				return
			case dataInMessage := <-p.portforwardDataInChannel:
				// Make this request locally, and then return that info to the user
				if _, err := io.Copy(dataStream, bytes.NewReader(dataInMessage)); err != nil {
					p.logger.Error(fmt.Errorf("error writing to data stream: %s", err))
					p.doneChan <- true
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
			case <-p.tmb.Dying():
				return
			case errorInMessage := <-p.portforwardErrorInChannel:
				// Make this request locally, and then return that info to the user
				if _, err := io.Copy(errorStream, bytes.NewReader(errorInMessage)); err != nil {
					p.logger.Error(fmt.Errorf("error writing to error stream: %s", err))

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
			case <-p.tmb.Dying():
				return
			default:
				buf := make([]byte, DataStreamBufferSize)
				n, err := dataStream.Read(buf)
				if err != nil {
					if err != io.EOF {
						rerr := fmt.Errorf("error reading data from data stream: %s", err)
						p.logger.Error(rerr)
					}

					content, err := p.wrapStreamMessageContent([]byte{}, portforwardRequestId)
					if err != nil {
						p.logger.Error(err)

						// Alert on our done channel
						p.doneChan <- true
					}

					// Send our end message
					message := smsg.StreamMessage{
						Type:           string(smsg.PortForwardEnd),
						RequestId:      requestId,
						LogId:          logId,
						SequenceNumber: dataSeqNumber,
						Content:        content,
					}
					p.streamOutputChan <- message

					p.doneChan <- true
					return
				}

				// Send this data back to the bastion
				content, err := p.wrapStreamMessageContent(buf[:n], portforwardRequestId)
				if err != nil {
					p.logger.Error(err)

					// Alert on our done channel
					p.doneChan <- true
				}

				message := smsg.StreamMessage{
					Type:           string(DataPortForward),
					RequestId:      requestId,
					LogId:          logId,
					SequenceNumber: dataSeqNumber,
					Content:        content,
				}
				p.streamOutputChan <- message
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
			case <-p.tmb.Dying():
				return
			default:
				buf := make([]byte, ErrorStreamBufferSize)
				n, err := errorStream.Read(buf)
				if err != nil {
					if err != io.EOF {
						rerr := fmt.Errorf("error reading data from error stream: %s", err)
						p.logger.Error(rerr)
					}

					// Alert on our done channel
					p.doneChan <- true
					return
				}

				content, err := p.wrapStreamMessageContent(buf[:n], portforwardRequestId)
				if err != nil {
					p.logger.Error(err)

					// Alert on our done channel
					p.doneChan <- true
				}

				message := smsg.StreamMessage{
					Type:           string(ErrorPortForward),
					RequestId:      requestId,
					LogId:          logId,
					SequenceNumber: errorSeqNumber,
					Content:        content,
				}
				p.streamOutputChan <- message
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
			case <-p.tmb.Dying():
				return
			case <-p.doneChan:
				return
			}
		}
	}()

	return nil
}

func (p *PortForwardRequest) wrapStreamMessageContent(content []byte, portforwardRequestId string) (string, error) {
	streamMessageToSend := KubePortForwardStreamMessageContent{
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

func (p *PortForwardAction) StartPortForward(startPortForwardRequest KubePortForwardStartActionPayload) (string, []byte, error) {
	// Update our object to keep track of the pod and url information
	p.DataHeaders = startPortForwardRequest.DataHeaders
	p.ErrorHeaders = startPortForwardRequest.ErrorHeaders
	p.Endpoint = startPortForwardRequest.Endpoint
	p.logId = startPortForwardRequest.LogId
	p.requestId = startPortForwardRequest.RequestId
	p.doneChan = make(chan bool, 1)

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
	streamCh, protocolSelected, err := dialer.Dial(kubeutilsdaemon.PortForwardProtocolV1Name)
	if err != nil {
		rerr := fmt.Errorf("error dialing portforward spdy stream: %s", err)
		p.logger.Error(rerr)

		// Let the user know about this error
		p.sendReadyMessage(err.Error())
	} else {
		p.logger.Infof("Dial successful. Selected protocol: %s", protocolSelected)

		// Let the user know we are ready
		p.sendReadyMessage("")
	}

	// Save the streamCh to use later
	p.streamCh = streamCh

	return string(StartPortForward), []byte{}, nil
}

func (p *PortForwardAction) sendReadyMessage(errorMessage string) {
	message := smsg.StreamMessage{
		Type:           string(ReadyPortForward),
		RequestId:      p.requestId,
		LogId:          p.logId,
		SequenceNumber: 0,
		Content:        errorMessage,
	}
	p.streamOutputChan <- message
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
