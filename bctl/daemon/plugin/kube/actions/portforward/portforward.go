package portforward

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"

	"golang.org/x/build/kubernetes/api"
	"k8s.io/apimachinery/pkg/util/httpstream"
	spdystream "k8s.io/apimachinery/pkg/util/httpstream/spdy"
)

var performHandshake = func(req *http.Request, w http.ResponseWriter, serverProtocols []string) (string, error) {
	return httpstream.Handshake(req, w, serverProtocols)
}

var getUpgradedConnection = func(w http.ResponseWriter, req *http.Request, handler httpstream.NewStreamHandler, pingPeriod time.Duration) httpstream.Connection {
	upgrader := spdystream.NewResponseUpgraderWithPings(kubeutils.DefaultStreamCreationTimeout)
	return upgrader.UpgradeResponse(w, req, handler)
}

type PortForwardRequest struct {
	streamMessageContent portforward.KubePortForwardStreamMessageContent
	streamMessage        smsg.StreamMessage
}
type PortForwardAction struct {
	logger          *logger.Logger
	requestId       string
	logId           string
	commandBeingRun string

	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage
	ksInputChan     chan plugin.ActionWrapper

	streamPairsLock       sync.RWMutex
	streamPairs           map[string]*httpStreamPair
	streamCreationTimeout time.Duration
	endpoint              string

	streamChan chan PortForwardRequest
}

// httpStreamPair represents the error and data streams for a port
// forwarding request.
type httpStreamPair struct {
	lock        sync.RWMutex
	requestID   string
	dataStream  httpstream.Stream
	errorStream httpstream.Stream
	complete    chan struct{}
}

func New(logger *logger.Logger,
	requestId string,
	logId string,
	command string) (*PortForwardAction, chan plugin.ActionWrapper) {

	portForward := &PortForwardAction{
		logger:                logger,
		requestId:             requestId,
		logId:                 logId,
		commandBeingRun:       command,
		outputChan:            make(chan plugin.ActionWrapper, 10),
		streamInputChan:       make(chan smsg.StreamMessage, 10),
		ksInputChan:           make(chan plugin.ActionWrapper, 10),
		streamPairs:           make(map[string]*httpStreamPair),
		streamCreationTimeout: kubeutils.DefaultStreamCreationTimeout,
	}

	return portForward, portForward.outputChan
}

func (p *PortForwardAction) ReceiveKeysplitting(wrappedAction plugin.ActionWrapper) {
	p.ksInputChan <- wrappedAction
}

func (p *PortForwardAction) ReceiveStream(stream smsg.StreamMessage) {

	// may be hearing about this via old or new language, depending on what we asked for
	if stream.Type == smsg.ReadyPortForward || stream.Type == smsg.Ready {
		// If this is our ready message, send to our ready channel
		p.streamInputChan <- stream
		return
	}

	// Unmarshal our content
	var kubePortforwardStreamMessageContent portforward.KubePortForwardStreamMessageContent
	contentBytes, _ := base64.StdEncoding.DecodeString(stream.Content)
	err := json.Unmarshal(contentBytes, &kubePortforwardStreamMessageContent)
	if err != nil {
		p.logger.Error(fmt.Errorf("error unmarshalling stream output for portforward action: %+v", err))
		return
	}

	p.streamChan <- PortForwardRequest{
		streamMessageContent: kubePortforwardStreamMessageContent,
		streamMessage:        stream,
	}
}

func (p *PortForwardAction) Start(tmb *tomb.Tomb, writer http.ResponseWriter, request *http.Request) error {
	// Set our endpoint
	p.endpoint = request.URL.String()

	// Let Bastion know we want to start a port forward session
	// create error and data stream headers
	errorHeaders := map[string]string{}
	errorHeaders[kubeutils.StreamType] = kubeutils.StreamTypeError

	dataHeaders := map[string]string{}
	dataHeaders[kubeutils.StreamType] = kubeutils.StreamTypeData

	// Let Bastion know we want this stream
	payload := portforward.KubePortForwardStartActionPayload{
		RequestId:            p.requestId,
		StreamMessageVersion: smsg.CurrentSchema,
		LogId:                p.logId,
		ErrorHeaders:         errorHeaders,
		DataHeaders:          dataHeaders,
		Endpoint:             p.endpoint,
		CommandBeingRun:      p.commandBeingRun,
	}
	payloadBytes, _ := json.Marshal(payload)
	p.outputChan <- plugin.ActionWrapper{
		Action:        string(portforward.StartPortForward),
		ActionPayload: payloadBytes,
	}

	// Now wait for the ready message, incase we need to bubble up an error to the user
readyMessageLoop:
	for {
		streamMessage := <-p.streamInputChan
		// may be hearing about this via old or new language, depending on what we asked for
		if streamMessage.Type == smsg.ReadyPortForward || streamMessage.Type == smsg.Ready {
			// See if we have an error to bubble up to the user
			if len(streamMessage.Content) != 0 {
				bubbleUpError(writer, streamMessage.Content)

				p.sendCloseMessage()
				return fmt.Errorf("error starting portforward stream: %s", streamMessage.Content)
			}
			break readyMessageLoop
		}
	}

	// Perform our http handshake
	_, err := performHandshake(request, writer, []string{kubeutils.PortForwardProtocolV1Name})
	if err != nil {
		return fmt.Errorf("could not perform http handshake: %v", err.Error())
	}

	// Now create our streamChan (where kubectl requests will come in)
	streamChan := make(chan httpstream.Stream, 1)

	// Upgrade the response
	conn := getUpgradedConnection(writer, request, p.httpStreamReceived(context.TODO(), streamChan), kubeutils.DefaultStreamCreationTimeout)
	if conn == nil {
		return fmt.Errorf("unable to upgrade websocket connection")
	}
	conn.SetIdleTimeout(kubeutils.DefaultIdleTimeout)
	defer conn.Close()

	// Now listen for incoming kubectl portforward requests in the background
	go func() {
		for {
			select {
			case <-tmb.Dying():
				return
			case <-conn.CloseChan():
				return
			case stream := <-streamChan:
				// Extract the requestId and streamType from the stream
				requestID, err := p.requestID(stream)
				if err != nil {
					p.logger.Error(fmt.Errorf("failed to parse request id: %v", err))
					return
				}
				streamType := stream.Headers().Get(kubeutils.StreamType)
				p.logger.Infof("Received new stream %v of type %v.", requestID, streamType)

				// Now attempt to make our stream pair (error, data)
				portforwardSession, created := p.getStreamPair(requestID)

				// If this was a new stream pair that was created, start a go routine to ensure it finishes (i.e. gets the error/data strema)
				if created {
					go p.monitorStreamPair(portforwardSession, time.After(p.streamCreationTimeout))
				}

				// Attempt to add the stream, so we can join the two streams
				if complete, err := portforwardSession.add(stream); err != nil {
					msg := fmt.Sprintf("error processing stream for request %s: %v", requestID, err)
					portforwardSession.printError(msg)
				} else if complete {
					go p.portForward(portforwardSession)
				}
			}
		}
	}()

	// Keep this context till the user exits the http session
	// Keep the connection alive till we get a closeChan messsage, then close the context as well
	select {
	case <-tmb.Dying():
		break
	case <-conn.CloseChan():
		p.logger.Info("Portforwarding context finished. Sending close message to portforward action")
		break
	}

	p.sendCloseMessage()
	return nil
}

// portForward invokes the portForwardProxy's forwarder.PortForward
// function for the given stream pair.
func (p *PortForwardAction) portForward(portforwardSession *httpStreamPair) {
	defer portforwardSession.dataStream.Close()
	defer portforwardSession.errorStream.Close()

	portString := portforwardSession.dataStream.Headers().Get(kubeutils.PortHeader)
	port, _ := strconv.ParseInt(portString, 10, 32)

	p.logger.Infof("Forwarding to port %v. Request: %v.", portString, portforwardSession.requestID)
	err := p.forwardStreamPair(portforwardSession, port)
	p.sendCloseRequestMessage(portforwardSession.requestID)
	p.logger.Infof("Completed forwarding port %v. Request: %v.", portString, portforwardSession.requestID)

	if err != nil {
		msg := fmt.Errorf("error forwarding port %d to pod ?: %v", port, err)
		p.logger.Error(msg)
	}
}

func (p *PortForwardAction) sendCloseRequestMessage(portforwardingRequestId string) {
	// Now send this data to Bastion
	payload := portforward.KubePortForwardStopRequestActionPayload{
		RequestId:            p.requestId,
		LogId:                p.logId,
		PortForwardRequestId: portforwardingRequestId,
	}
	payloadBytes, _ := json.Marshal(payload)
	p.outputChan <- plugin.ActionWrapper{
		Action:        string(portforward.StopPortForwardRequest),
		ActionPayload: payloadBytes,
	}
}

func (p *PortForwardAction) sendCloseMessage() {
	// Now send this data to Bastion
	payload := portforward.KubePortForwardStopActionPayload{
		RequestId: p.requestId,
		LogId:     p.logId,
	}
	payloadBytes, _ := json.Marshal(payload)
	p.outputChan <- plugin.ActionWrapper{
		Action:        string(portforward.StopPortForward),
		ActionPayload: payloadBytes,
	}
}

func (p *PortForwardAction) forwardStreamPair(portforwardSession *httpStreamPair, remotePort int64) error {
	// Make a done channel
	// there are 4 instances where you push true to the done channel.
	// processErrorMessage and processDataMessage, the error on read, and PortForwardEnd could all theoretically happen at the same time (I think)
	// So to remedy that I rounded up to 5.
	doneChan := make(chan bool, 5)

	// Set up the go routine to push error data to Bastion
	go func() {
		defer portforwardSession.errorStream.Close()
		for {
			select {
			case <-doneChan:
				return
			default:
				buf := make([]byte, portforward.ErrorStreamBufferSize)
				n, err := portforwardSession.errorStream.Read(buf)
				if err == io.EOF {
					// Do not close the stream if we close the errorstream
					return
				}

				// Now send this data to Bastion
				payload := portforward.KubePortForwardActionPayload{
					RequestId:            p.requestId,
					LogId:                p.logId,
					Data:                 buf[:n],
					PortForwardRequestId: portforwardSession.requestID,
				}
				payloadBytes, _ := json.Marshal(payload)
				p.outputChan <- plugin.ActionWrapper{
					Action:        string(portforward.ErrorPortForward),
					ActionPayload: payloadBytes,
				}
			}
		}
	}()

	// Set up the go routine to push regular data to Bastion from the data stream
	go func() {
		defer portforwardSession.dataStream.Close()
		for {
			select {
			case <-doneChan:
				return
			default:
				buf := make([]byte, portforward.DataStreamBufferSize)
				n, err := portforwardSession.dataStream.Read(buf)
				if err != nil || err != io.EOF {
					p.logger.Error(fmt.Errorf("received error on datastream: %s", err))

					doneChan <- true
					return
				}

				// Now send this data to Bastion
				payload := portforward.KubePortForwardActionPayload{
					RequestId:            p.requestId,
					LogId:                p.logId,
					Data:                 buf[:n],
					PortForwardRequestId: portforwardSession.requestID,
					PodPort:              remotePort,
				}
				payloadBytes, _ := json.Marshal(payload)
				p.outputChan <- plugin.ActionWrapper{
					Action:        string(portforward.DataInPortForward),
					ActionPayload: payloadBytes,
				}
			}
		}
	}()

	// We have to keep track of error and data seq numbers and keep a buffer
	expectedDataSeqNumber := 0
	expectedErrorSeqNumber := 0
	dataBuffer := make(map[int][]byte)
	errorBuffer := make(map[int][]byte)

	// Set up our message processors
	processDataMessage := func(content []byte) {
		if _, err := io.Copy(portforwardSession.dataStream, bytes.NewReader(content)); err != nil {
			rerr := fmt.Errorf("error writing to stream data: %s", err)
			p.logger.Error(rerr)

			doneChan <- true
		}
		expectedDataSeqNumber += 1
	}

	processErrorMessage := func(content []byte) {
		if _, err := io.Copy(portforwardSession.errorStream, bytes.NewReader(content)); err != nil {
			rerr := fmt.Errorf("error writing to stream error: %s", err)
			p.logger.Error(rerr)

			// Do not close the stream if the error stream ends
			doneChan <- true
		}
		expectedErrorSeqNumber += 1
	}

	// Set up the function to listen to bastion messages and push to the user
	for {
		select {
		case <-doneChan:
			// Return
			return nil
		case portForwardRequest := <-p.streamChan:
			// contentBytes, _ := base64.StdEncoding.DecodeString(streamMessage.Content)

			if portForwardRequest.streamMessage.Type == smsg.Data {
				// Check our seqNumber
				if portForwardRequest.streamMessage.SequenceNumber == expectedDataSeqNumber {
					processDataMessage(portForwardRequest.streamMessageContent.Content)
				} else {
					// Update our buffer
					dataBuffer[portForwardRequest.streamMessage.SequenceNumber] = portForwardRequest.streamMessageContent.Content
				}

				// Always attempt to processes out of order messages
				outOfOrderDataContent, ok := dataBuffer[expectedDataSeqNumber]
				for ok {
					// Keep pulling older messages
					processDataMessage(outOfOrderDataContent)
					outOfOrderDataContent, ok = dataBuffer[expectedDataSeqNumber]
				}

				if !portForwardRequest.streamMessage.More {
					// Alert on our done chan
					doneChan <- true
				}

			} else if portForwardRequest.streamMessage.Type == smsg.Error {
				if portForwardRequest.streamMessage.SequenceNumber == expectedErrorSeqNumber {
					processErrorMessage(portForwardRequest.streamMessageContent.Content)
				} else {
					// Update our buffer
					errorBuffer[portForwardRequest.streamMessage.SequenceNumber] = portForwardRequest.streamMessageContent.Content
				}

				// Always attempt to process out of order messages
				outOfOrderErrorContent, ok := errorBuffer[expectedErrorSeqNumber]
				for ok {
					// Keep pulling older messages
					processErrorMessage(outOfOrderErrorContent)
					outOfOrderErrorContent, ok = errorBuffer[expectedErrorSeqNumber]
				}

			} else {
				p.logger.Errorf("unhandled stream type: %s", portForwardRequest.streamMessage.Type)
			}
		}
	}
}

// requestID returns the request id for stream.
func (p *PortForwardAction) requestID(stream httpstream.Stream) (string, error) {
	requestID := stream.Headers().Get(kubeutils.PortForwardRequestIDHeader)
	if len(requestID) == 0 {
		return "", errors.New("port forwarding is not supported")
	}
	return requestID, nil
}

func bubbleUpError(writer http.ResponseWriter, content string) {
	// Bubble up the error to the user
	// Ref: https://pkg.go.dev/golang.org/x/build/kubernetes/api#Status
	toReturn := api.Status{
		Message: content,
		Status:  api.StatusFailure,
		Code:    http.StatusForbidden,
		Reason:  "Forbidden",
	}
	toReturnMarshal, err := json.Marshal(toReturn)
	if err != nil {
		// Best effort bubble up
		writer.WriteHeader(http.StatusInternalServerError)
		writer.Write([]byte(err.Error()))
	} else {
		writer.WriteHeader(http.StatusForbidden)
		writer.Header().Set("Content-Type", "application/json")
		writer.Write(toReturnMarshal)
	}
}
