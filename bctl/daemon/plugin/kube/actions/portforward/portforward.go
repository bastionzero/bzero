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

var getUpgradedConnection = func(w http.ResponseWriter, req *http.Request, streamChan chan httpstream.Stream, pingPeriod time.Duration) httpstream.Connection {
	upgrader := spdystream.NewResponseUpgraderWithPings(kubeutils.DefaultStreamCreationTimeout)
	return upgrader.UpgradeResponse(w, req, httpStreamReceived(context.TODO(), streamChan))
}

type RequestMapStruct struct {
	streamMessageContent portforward.KubePortForwardStreamMessageContent
	streamMessage        smsg.StreamMessage
}
type PortForwardAction struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	requestId       string
	logId           string
	commandBeingRun string

	cancelChan      chan struct{} // internal channel for shutting everything down
	doneChan        chan struct{}
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage

	streamPairsLock       sync.RWMutex
	streamPairs           map[string]*httpStreamPair
	streamCreationTimeout time.Duration
	endpoint              string

	// Map of portforardId <-> PortForwardSubAction
	requestMap map[string]chan RequestMapStruct
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

func New(
	logger *logger.Logger,
	outputChan chan plugin.ActionWrapper,
	doneChan chan struct{},
	requestId string,
	logId string,
	command string) *PortForwardAction {

	return &PortForwardAction{
		logger:                logger,
		requestId:             requestId,
		logId:                 logId,
		commandBeingRun:       command,
		cancelChan:            make(chan struct{}),
		doneChan:              doneChan,
		outputChan:            outputChan,
		streamInputChan:       make(chan smsg.StreamMessage, 10),
		streamPairs:           make(map[string]*httpStreamPair),
		streamCreationTimeout: kubeutils.DefaultStreamCreationTimeout,
		requestMap:            make(map[string]chan RequestMapStruct),
	}
}

func (p *PortForwardAction) Kill() {
	close(p.cancelChan)
	<-p.doneChan
}

func (p *PortForwardAction) ReceiveKeysplitting(actionPayload []byte) {}

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
		p.logger.Error(fmt.Errorf("error unmarshalling stream output for portforward action: %s", err))
		return
	}

	// First get the stream
	streamChan, ok := p.requestMap[kubePortforwardStreamMessageContent.PortForwardRequestId]
	if !ok {
		p.logger.Error(fmt.Errorf("unable to find stream chan for request: %s", kubePortforwardStreamMessageContent.PortForwardRequestId))
		return
	}
	streamChan <- RequestMapStruct{
		streamMessageContent: kubePortforwardStreamMessageContent,
		streamMessage:        stream,
	}
}

func (p *PortForwardAction) Start(writer http.ResponseWriter, request *http.Request) error {
	defer close(p.doneChan)

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
	p.outbox(portforward.StartPortForward, payload)

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
		return fmt.Errorf("could not perform http handshake: %s", err)
	}

	// Now create our streamChan (where kubectl requests will come in)
	streamChan := make(chan httpstream.Stream, 1)

	// Upgrade the response
	conn := getUpgradedConnection(writer, request, streamChan, kubeutils.DefaultStreamCreationTimeout)
	if conn == nil {
		return fmt.Errorf("unable to upgrade websocket connection")
	}
	conn.SetIdleTimeout(kubeutils.DefaultIdleTimeout)
	defer conn.Close()

	// Now listen for incoming kubectl portforward requests in the background
	defer p.sendCloseMessage()
	for {
		select {
		case <-p.cancelChan:
			return nil
		case <-conn.CloseChan():
			return nil
		case <-request.Context().Done():
			return nil
		case stream := <-streamChan:
			// Extract the requestId and streamType from the stream
			requestID, err := p.requestID(stream)
			if err != nil {
				p.logger.Errorf("failed to parse request id: %s", err)
				return nil
			}
			streamType := stream.Headers().Get(kubeutils.StreamType)
			p.logger.Infof("Received new stream %s of type %s", requestID, streamType)

			// Now attempt to make our stream pair (error, data)
			portforwardSession, created := p.getStreamPair(requestID)

			// If this was a new stream pair that was created, start a go routine to ensure it finishes (i.e. gets the error/data stream)
			if created {
				go p.monitorStreamPair(portforwardSession, time.After(p.streamCreationTimeout))
			}

			// Attempt to add the stream, so we can join the two streams
			if complete, err := portforwardSession.add(stream); err != nil {
				msg := fmt.Sprintf("error processing stream for request %s: %s", requestID, err)
				portforwardSession.printError(msg)
			} else if complete {
				go p.portForward(portforwardSession)
			}
		}
	}
}

// portForward invokes the portForwardProxy's forwarder.PortForward
// function for the given stream pair.
func (p *PortForwardAction) portForward(portforwardSession *httpStreamPair) {
	portString := portforwardSession.dataStream.Headers().Get(kubeutils.PortHeader)
	port, _ := strconv.ParseInt(portString, 10, 32)

	p.logger.Infof("Forwarding to port %s, Request: %s", portString, portforwardSession.requestID)
	if err := p.forwardStreamPair(portforwardSession, port); err != nil {
		msg := fmt.Errorf("error forwarding port %d to pod: %s", port, err)
		p.logger.Error(msg)
	}

	p.sendCloseRequestMessage(portforwardSession.requestID)
	p.logger.Infof("Completed forwarding port %s. Request: %s", portString, portforwardSession.requestID)
}

func (p *PortForwardAction) sendCloseRequestMessage(portforwardingRequestId string) {
	// Now send this data to Bastion
	payload := portforward.KubePortForwardStopRequestActionPayload{
		RequestId:            p.requestId,
		LogId:                p.logId,
		PortForwardRequestId: portforwardingRequestId,
	}
	p.outbox(portforward.StopPortForwardRequest, payload)
}

func (p *PortForwardAction) sendCloseMessage() {
	// Now send this data to Bastion
	payload := portforward.KubePortForwardStopActionPayload{
		RequestId: p.requestId,
		LogId:     p.logId,
	}
	p.outbox(portforward.StopPortForward, payload)
}

func (p *PortForwardAction) forwardStreamPair(portforwardSession *httpStreamPair, remotePort int64) error {
	// Make and update the stream channel for this requestId
	p.requestMap[portforwardSession.requestID] = make(chan RequestMapStruct)

	// set up our defers to close our streams
	defer portforwardSession.dataStream.Close()
	defer portforwardSession.errorStream.Close()

	var tmb tomb.Tomb

	tmb.Go(func() error {
		// Set up the go routine to push error data to Bastion
		tmb.Go(func() error {

			buf := make([]byte, portforward.ErrorStreamBufferSize)
			for {
				select {
				case <-p.cancelChan:
					return nil
				case <-tmb.Dying():
					return nil
				default:
					if n, err := portforwardSession.errorStream.Read(buf); !p.tmb.Alive() {
						return nil
					} else if err == io.EOF {
						// Do not close the stream if we close the errorstream
						return nil
					} else {
						// Now send this data to Bastion
						payload := portforward.KubePortForwardActionPayload{
							RequestId:            p.requestId,
							LogId:                p.logId,
							Data:                 buf[:n],
							PortForwardRequestId: portforwardSession.requestID,
						}
						p.outbox(portforward.ErrorPortForward, payload)
					}
				}
			}
		})

		// Set up the go routine to push regular data to Bastion from the data stream
		buf := make([]byte, portforward.DataStreamBufferSize)
		for {
			select {
			case <-p.cancelChan:
				return nil
			case <-tmb.Dying():
				return nil
			default:
				if n, err := portforwardSession.dataStream.Read(buf); !tmb.Alive() {
					return nil
				} else if err != nil {
					if err != io.EOF {
						p.logger.Error(fmt.Errorf("received error on datastream: %s", err))
					}
					return nil
				} else {
					// Now send this data to Bastion
					payload := portforward.KubePortForwardActionPayload{
						RequestId:            p.requestId,
						LogId:                p.logId,
						Data:                 buf[:n],
						PortForwardRequestId: portforwardSession.requestID,
						PodPort:              remotePort,
					}
					p.outbox(portforward.DataInPortForward, payload)
				}
			}
		}
	})

	// We have to keep track of error and data seq numbers and keep a buffer
	expectedDataSeqNumber := 0
	expectedErrorSeqNumber := 0
	dataBuffer := make(map[int]RequestMapStruct)
	errorBuffer := make(map[int]RequestMapStruct)

	// Set up our message processors
	processDataMessage := func(content []byte, more bool) {
		if _, err := io.Copy(portforwardSession.dataStream, bytes.NewReader(content)); err != nil {
			p.logger.Errorf("error writing to stream data: %s", err)
			tmb.Kill(nil)
		} else if !more {
			tmb.Kill(nil)
		}
		expectedDataSeqNumber += 1
	}

	processErrorMessage := func(content []byte) {
		if _, err := io.Copy(portforwardSession.errorStream, bytes.NewReader(content)); err != nil {
			p.logger.Errorf("error writing to stream error: %s", err)
			tmb.Kill(nil)
		}
		expectedErrorSeqNumber += 1
	}

	// Get our chan
	requestMapChannel, ok := p.requestMap[portforwardSession.requestID]
	if !ok {
		p.logger.Errorf("error getting stream for request: %s", portforwardSession.requestID)
		return errors.New("unable to find stream channel")
	}

	// Set up the function to listen to bastion messages and push to the user
	for {
		select {
		case <-tmb.Dying():
			// Delete the stream pair from our mapping
			delete(p.requestMap, portforwardSession.requestID)
			return nil
		case requestMapStruct := <-requestMapChannel:
			// contentBytes, _ := base64.StdEncoding.DecodeString(streamMessage.Content)

			if requestMapStruct.streamMessage.Type == smsg.Data {
				// Check our seqNumber
				if requestMapStruct.streamMessage.SequenceNumber == expectedDataSeqNumber {
					processDataMessage(requestMapStruct.streamMessageContent.Content, requestMapStruct.streamMessage.More)
				} else {
					// Update our buffer
					dataBuffer[requestMapStruct.streamMessage.SequenceNumber] = requestMapStruct
				}

				// Always attempt to processes out of order messages
				for oooRequest, ok := dataBuffer[expectedDataSeqNumber]; ok; oooRequest, ok = dataBuffer[expectedDataSeqNumber] {
					// Keep pulling older messages
					processDataMessage(oooRequest.streamMessageContent.Content, oooRequest.streamMessage.More)
					oooRequest, ok = dataBuffer[expectedDataSeqNumber]
				}

			} else if requestMapStruct.streamMessage.Type == smsg.Error {
				if requestMapStruct.streamMessage.SequenceNumber == expectedErrorSeqNumber {
					processErrorMessage(requestMapStruct.streamMessageContent.Content)
				} else {
					// Update our buffer
					errorBuffer[requestMapStruct.streamMessage.SequenceNumber] = requestMapStruct
				}

				// Always attempt to process out of order messages
				outOfOrderErrorRequest, ok := errorBuffer[expectedErrorSeqNumber]
				for ok {
					// Keep pulling older messages
					processErrorMessage([]byte(outOfOrderErrorRequest.streamMessageContent.Content))
					outOfOrderErrorRequest, ok = errorBuffer[expectedErrorSeqNumber]
				}

			} else {
				p.logger.Errorf("unhandled stream type: %s", requestMapStruct.streamMessage.Type)
			}
		}
	}
}

func (p *PortForwardAction) outbox(action portforward.PortForwardSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	p.outputChan <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: &payloadBytes,
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

// getStreamPair returns a httpStreamPair for requestID. This creates a
// new pair if one does not yet exist for the requestID. The returned bool is
// true if the pair was created.
func (p *PortForwardAction) getStreamPair(requestID string) (*httpStreamPair, bool) {
	p.streamPairsLock.Lock()
	defer p.streamPairsLock.Unlock()

	if portforwardStreamPair, ok := p.streamPairs[requestID]; ok {
		p.logger.Infof("Request %s, found existing stream pair", requestID)
		return portforwardStreamPair, false
	}

	p.logger.Infof("Request %s, creating new stream pair.", requestID)

	portforwardStreamPair := newPortForwardPair(requestID)
	p.streamPairs[requestID] = portforwardStreamPair

	return portforwardStreamPair, true
}

// newPortForwardPair creates a new httpStreamPair.
func newPortForwardPair(requestID string) *httpStreamPair {
	return &httpStreamPair{
		requestID: requestID,
		complete:  make(chan struct{}),
	}
}

// add adds the stream to the httpStreamPair. If the pair already
// contains a stream for the new stream's type, an error is returned. add
// returns true if both the data and error streams for this pair have been
// received.
func (p *httpStreamPair) add(stream httpstream.Stream) (bool, error) {
	p.lock.Lock()
	defer p.lock.Unlock()

	switch stream.Headers().Get(kubeutils.StreamType) {
	case kubeutils.StreamTypeError:
		if p.errorStream != nil {
			return false, errors.New("error stream already assigned")
		}
		p.errorStream = stream
	case kubeutils.StreamTypeData:
		if p.dataStream != nil {
			return false, errors.New("data stream already assigned")
		}
		p.dataStream = stream
	}

	complete := p.errorStream != nil && p.dataStream != nil
	if complete {
		close(p.complete)
	}
	return complete, nil
}

// printError writes s to p.errorStream if p.errorStream has been set.
func (p *httpStreamPair) printError(s string) {
	p.lock.RLock()
	defer p.lock.RUnlock()
	if p.errorStream != nil {
		fmt.Fprint(p.errorStream, s)
	}
}

// monitorStreamPair waits for the pair to receive both its error and data
// streams, or for the timeout to expire (whichever happens first), and then
// removes the pair.
func (p *PortForwardAction) monitorStreamPair(portforwardStreamPair *httpStreamPair, timeout <-chan time.Time) {
	select {
	case <-timeout:
		p.logger.Error(fmt.Errorf("request %s, timed out waiting for streams", portforwardStreamPair.requestID))
	case <-portforwardStreamPair.complete:
		p.logger.Infof("Request %s, successfully received error and data streams.", portforwardStreamPair.requestID)
	}
	p.removeStreamPair(portforwardStreamPair.requestID)
}

// removeStreamPair removes the stream pair identified by requestID from streamPairs.
func (p *PortForwardAction) removeStreamPair(requestID string) {
	p.streamPairsLock.Lock()
	defer p.streamPairsLock.Unlock()

	delete(p.streamPairs, requestID)
}

// httpStreamReceived is the httpstream.NewStreamHandler for port
// forward streams. It checks each stream's port and stream type headers,
// rejecting any streams that with missing or invalid values. Each valid
// stream is sent to the streams channel.
func httpStreamReceived(ctx context.Context, streams chan httpstream.Stream) func(httpstream.Stream, <-chan struct{}) error {
	return func(stream httpstream.Stream, replySent <-chan struct{}) error {
		// make sure it has a valid port header
		portString := stream.Headers().Get(kubeutils.PortHeader)
		if len(portString) == 0 {
			return fmt.Errorf("%q header is required", kubeutils.PortHeader)
		}
		port, err := strconv.ParseUint(portString, 10, 16)
		if err != nil {
			return fmt.Errorf("unable to parse %q as a port: %v", portString, err)
		}
		if port < 1 {
			return fmt.Errorf("port %q must be > 0", portString)
		}

		// make sure it has a valid stream type header
		streamType := stream.Headers().Get(kubeutils.StreamType)
		if len(streamType) == 0 {
			return fmt.Errorf("%q header is required", kubeutils.StreamType)
		}
		if streamType != kubeutils.StreamTypeError && streamType != kubeutils.StreamTypeData {
			return fmt.Errorf("invalid stream type %q", streamType)
		}

		select {
		case streams <- stream:
			return nil
		case <-ctx.Done():
			return errors.New("request has been cancelled")
		}
	}
}
