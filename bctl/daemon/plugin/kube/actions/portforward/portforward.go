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

	"golang.org/x/build/kubernetes/api"
	"k8s.io/apimachinery/pkg/util/httpstream"
	spdystream "k8s.io/apimachinery/pkg/util/httpstream/spdy"
)

type RequestMapStruct struct {
	streamMessageContent portforward.KubePortForwardStreamMessageContent
	streamMessage        smsg.StreamMessage
}
type PortForwardAction struct {
	logger *logger.Logger

	requestId       string
	logId           string
	commandBeingRun string

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

func New(logger *logger.Logger,
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
		doneChan:              doneChan,
		outputChan:            outputChan,
		streamInputChan:       make(chan smsg.StreamMessage, 10),
		streamPairs:           make(map[string]*httpStreamPair),
		streamCreationTimeout: kubeutils.DefaultStreamCreationTimeout,
		requestMap:            make(map[string]chan RequestMapStruct),
	}
}

func (p *PortForwardAction) Kill() {
	close(p.doneChan)
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
		p.logger.Error(fmt.Errorf("error unmarshalling stream output for portforward action: %+v", err))
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
	p.outputChan <- plugin.ActionWrapper{
		Action:        string(portforward.StartPortForward),
		ActionPayload: payload,
	}

	// Now wait for the ready message, incase we need to bubble up an error to the user
waitForReadyMessage:
	for {
		select {
		case streamMessage := <-p.streamInputChan:
			// may be hearing about this via old or new language, depending on what we asked for
			if streamMessage.Type == smsg.ReadyPortForward || streamMessage.Type == smsg.Ready {
				// See if we have an error to bubble up to the user
				if len(streamMessage.Content) != 0 {
					bubbleUpError(writer, streamMessage.Content)

					p.sendCloseMessage()
					return fmt.Errorf("error starting portforward stream: %s", streamMessage.Content)
				}
				break waitForReadyMessage
			}
		case <-time.After(15 * time.Second):
			return fmt.Errorf("failed to receive ready message in time")
		}
	}

	// Perform our http handshake
	_, err := httpstream.Handshake(request, writer, []string{kubeutils.PortForwardProtocolV1Name})
	if err != nil {
		return fmt.Errorf("could not perform http handshake: %v", err.Error())
	}

	// Now create our streamChan (where kubectl requests will come in)
	streamChan := make(chan httpstream.Stream, 1)

	// Upgrade the response
	upgrader := spdystream.NewResponseUpgraderWithPings(kubeutils.DefaultStreamCreationTimeout)
	conn := upgrader.UpgradeResponse(writer, request, p.httpStreamReceived(context.TODO(), streamChan))
	if conn == nil {
		return fmt.Errorf("unable to upgrade websocket connection")
	}
	conn.SetIdleTimeout(kubeutils.DefaultIdleTimeout)

	defer p.sendCloseMessage()
	defer conn.Close()

	// Now listen for incoming kubectl portforward requests in the background
	for {
		select {
		case <-p.doneChan:
			return nil
		case <-conn.CloseChan():
			p.logger.Info("Portforwarding context finished. Sending close message to portforward action")
			p.sendCloseMessage()
			return nil
		case stream := <-streamChan:
			// Extract the requestId and streamType from the stream
			requestID, err := p.requestID(stream)
			if err != nil {
				p.logger.Errorf("failed to parse request id: %v", err)
				p.sendCloseMessage()
				return nil
			}
			streamType := stream.Headers().Get(kubeutils.StreamType)
			p.logger.Infof("Received new stream %v of type %v.", requestID, streamType)

			// Now attempt to make our stream pair (error, data)
			if portforwardSession, created := p.getStreamPair(requestID); created {
				// If this was a new stream pair that was created, start a go routine to ensure it finishes (i.e. gets the error/data stream)
				go p.monitorStreamPair(portforwardSession, time.After(p.streamCreationTimeout))
			} else if complete, err := portforwardSession.add(stream); err != nil { // Attempt to add the stream, so we can join the two streams
				msg := fmt.Sprintf("error processing stream for request %s: %v", requestID, err)
				portforwardSession.printError(msg)
			} else if complete {
				go p.portForward(portforwardSession)
			}
		}
	}

	// Keep this context till the user exits the http session
	// Keep the connection alive till we get a closeChan messsage, then close the context as well
	// select {
	// case <-tmb.Dying():
	// 	break
	// case <-conn.CloseChan():
	// 	p.logger.Info("Portforwarding context finished. Sending close message to portforward action")
	// 	break
	// }

	// p.sendCloseMessage()
	// return nil
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
	p.outputChan <- plugin.ActionWrapper{
		Action:        string(portforward.StopPortForwardRequest),
		ActionPayload: payload,
	}
}

func (p *PortForwardAction) sendCloseMessage() {
	defer close(p.doneChan)

	// Now send this data to Bastion
	p.outputChan <- plugin.ActionWrapper{
		Action: string(portforward.StopPortForward),
		ActionPayload: portforward.KubePortForwardStopActionPayload{
			RequestId: p.requestId,
			LogId:     p.logId,
		},
	}
}

func (p *PortForwardAction) forwardStreamPair(portforwardSession *httpStreamPair, remotePort int64) error {
	// Make a done channel
	// there are 4 instances where you push true to the done channel.
	// processErrorMessage and processDataMessage, the error on read, and PortForwardEnd could all theoretically happen at the same time (I think)
	// So to remedy that I rounded up to 5.
	doneChan := make(chan bool, 5)

	// Make and update the stream channel for this requestId
	p.requestMap[portforwardSession.requestID] = make(chan RequestMapStruct)

	// Set up the go routine to push error data to Bastion
	go func() {
		defer portforwardSession.errorStream.Close()
		for {
			select {
			case <-p.doneChan:
				return
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
				p.outputChan <- plugin.ActionWrapper{
					Action:        string(portforward.ErrorPortForward),
					ActionPayload: payload,
				}
			}
		}

	}()

	// Set up the go routine to push regular data to Bastion from the data stream
	go func() {
		defer portforwardSession.dataStream.Close()
		for {
			select {
			case <-p.doneChan:
				return
			case <-doneChan:
				return
			default:
				buf := make([]byte, portforward.DataStreamBufferSize)
				n, err := portforwardSession.dataStream.Read(buf)
				if err != nil {
					if err != io.EOF {
						p.logger.Error(fmt.Errorf("received error on datastream: %s", err))
					}
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
				p.outputChan <- plugin.ActionWrapper{
					Action:        string(portforward.DataInPortForward),
					ActionPayload: payload,
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

	// Get our chan
	requestMapChannel, ok := p.requestMap[portforwardSession.requestID]
	if !ok {
		p.logger.Error(fmt.Errorf("error getting stream for request: %s", portforwardSession.requestID))
		return errors.New("unable to find stream channel")
	}

	// Set up the function to listen to bastion messages and push to the user
	for {
		select {
		case <-p.doneChan:
			return nil
		case <-doneChan:
			// Delete the stream pair from our mapping
			delete(p.requestMap, portforwardSession.requestID)

			// Return
			return nil
		case requestMapStruct := <-requestMapChannel:
			// contentBytes, _ := base64.StdEncoding.DecodeString(streamMessage.Content)

			switch requestMapStruct.streamMessage.Type {
			case smsg.Data:
				// Check our seqNumber
				if requestMapStruct.streamMessage.SequenceNumber == expectedDataSeqNumber {
					processDataMessage(requestMapStruct.streamMessageContent.Content)
				} else {
					// Update our buffer
					dataBuffer[requestMapStruct.streamMessage.SequenceNumber] = requestMapStruct.streamMessageContent.Content
				}

				// Always attempt to processes out of order messages
				for outOfOrderData, ok := dataBuffer[expectedDataSeqNumber]; ok; outOfOrderData, ok = dataBuffer[expectedDataSeqNumber] {
					processDataMessage(outOfOrderData)
				}

				if !requestMapStruct.streamMessage.More {
					// Alert on our done chan
					doneChan <- true
				}
			case smsg.Error:
				if requestMapStruct.streamMessage.SequenceNumber == expectedErrorSeqNumber {
					processErrorMessage(requestMapStruct.streamMessageContent.Content)
				} else {
					// Update our buffer
					errorBuffer[requestMapStruct.streamMessage.SequenceNumber] = requestMapStruct.streamMessageContent.Content
				}

				// Always attempt to process out of order messages
				for outOfOrderError, ok := errorBuffer[expectedDataSeqNumber]; ok; outOfOrderError, ok = errorBuffer[expectedDataSeqNumber] {
					processDataMessage(outOfOrderError)
				}
				// outOfOrderErrorContent, ok := errorBuffer[expectedErrorSeqNumber]
				// for ok {
				// 	// Keep pulling older messages
				// 	processErrorMessage(outOfOrderErrorContent)
				// 	outOfOrderErrorContent, ok = errorBuffer[expectedErrorSeqNumber]
				// }
			default:
				p.logger.Errorf("unhandled stream type: %s", requestMapStruct.streamMessage.Type)
			}
			// if requestMapStruct.streamMessage.Type == smsg.Data {
			// 	// Check our seqNumber
			// 	if requestMapStruct.streamMessage.SequenceNumber == expectedDataSeqNumber {
			// 		processDataMessage(requestMapStruct.streamMessageContent.Content)
			// 	} else {
			// 		// Update our buffer
			// 		dataBuffer[requestMapStruct.streamMessage.SequenceNumber] = requestMapStruct.streamMessageContent.Content
			// 	}

			// 	// Always attempt to processes out of order messages
			// 	outOfOrderDataContent, ok := dataBuffer[expectedDataSeqNumber]
			// 	for ok {
			// 		// Keep pulling older messages
			// 		processDataMessage(outOfOrderDataContent)
			// 		outOfOrderDataContent, ok = dataBuffer[expectedDataSeqNumber]
			// 	}

			// 	if !requestMapStruct.streamMessage.More {
			// 		// Alert on our done chan
			// 		doneChan <- true
			// 	}

			// } else if requestMapStruct.streamMessage.Type == smsg.Error {
			// 	if requestMapStruct.streamMessage.SequenceNumber == expectedErrorSeqNumber {
			// 		processErrorMessage(requestMapStruct.streamMessageContent.Content)
			// 	} else {
			// 		// Update our buffer
			// 		errorBuffer[requestMapStruct.streamMessage.SequenceNumber] = requestMapStruct.streamMessageContent.Content
			// 	}

			// 	// Always attempt to process out of order messages
			// 	outOfOrderErrorContent, ok := errorBuffer[expectedErrorSeqNumber]
			// 	for ok {
			// 		// Keep pulling older messages
			// 		processErrorMessage(outOfOrderErrorContent)
			// 		outOfOrderErrorContent, ok = errorBuffer[expectedErrorSeqNumber]
			// 	}

			// } else {
			// 	p.logger.Errorf("unhandled stream type: %s", requestMapStruct.streamMessage.Type)
			// }
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
