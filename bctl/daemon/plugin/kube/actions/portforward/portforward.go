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
	p.outputChan <- plugin.ActionWrapper{
		Action:        string(portforward.StartPortForward),
		ActionPayload: payload,
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
	_, err := httpstream.Handshake(request, writer, []string{kubeutils.PortForwardProtocolV1Name})
	if err != nil {
		return fmt.Errorf("could not perform http handshake: %s", err)
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

			// If this was a new stream pair that was created, start a go routine to ensure it finishes (i.e. gets the error/data strema)
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
	defer portforwardSession.dataStream.Close()
	defer portforwardSession.errorStream.Close()

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
	p.outputChan <- plugin.ActionWrapper{
		Action:        string(portforward.StopPortForwardRequest),
		ActionPayload: payload,
	}
}

func (p *PortForwardAction) sendCloseMessage() {
	// Now send this data to Bastion
	payload := portforward.KubePortForwardStopActionPayload{
		RequestId: p.requestId,
		LogId:     p.logId,
	}
	p.outputChan <- plugin.ActionWrapper{
		Action:        string(portforward.StopPortForward),
		ActionPayload: payload,
	}
}

func (p *PortForwardAction) forwardStreamPair(portforwardSession *httpStreamPair, remotePort int64) error {
	// Make and update the stream channel for this requestId
	p.requestMap[portforwardSession.requestID] = make(chan RequestMapStruct)

	var tmb tomb.Tomb

	tmb.Go(func() error {
		// Set up the go routine to push error data to Bastion
		tmb.Go(func() error {
			defer portforwardSession.errorStream.Close()

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
						p.outputChan <- plugin.ActionWrapper{
							Action:        string(portforward.ErrorPortForward),
							ActionPayload: payload,
						}
					}
				}
			}
		})

		// Set up the go routine to push regular data to Bastion from the data stream
		defer portforwardSession.dataStream.Close()

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
					p.outputChan <- plugin.ActionWrapper{
						Action:        string(portforward.DataInPortForward),
						ActionPayload: payload,
					}
				}
			}
		}
	})

	// We have to keep track of error and data seq numbers and keep a buffer
	expectedDataSeqNumber := 0
	expectedErrorSeqNumber := 0
	dataBuffer := make(map[int][]byte)
	errorBuffer := make(map[int][]byte)

	// Set up our message processors
	processDataMessage := func(content []byte) {
		if _, err := io.Copy(portforwardSession.dataStream, bytes.NewReader(content)); err != nil {
			p.logger.Errorf("error writing to stream data: %s", err)
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
					processDataMessage(requestMapStruct.streamMessageContent.Content)
				} else {
					// Update our buffer
					dataBuffer[requestMapStruct.streamMessage.SequenceNumber] = requestMapStruct.streamMessageContent.Content
				}

				// Always attempt to processes out of order messages
				outOfOrderDataContent, ok := dataBuffer[expectedDataSeqNumber]
				for ok {
					// Keep pulling older messages
					processDataMessage(outOfOrderDataContent)
					outOfOrderDataContent, ok = dataBuffer[expectedDataSeqNumber]
				}

				if !requestMapStruct.streamMessage.More {
					tmb.Kill(nil)
					return nil
				}

			} else if requestMapStruct.streamMessage.Type == smsg.Error {
				if requestMapStruct.streamMessage.SequenceNumber == expectedErrorSeqNumber {
					processErrorMessage(requestMapStruct.streamMessageContent.Content)
				} else {
					// Update our buffer
					errorBuffer[requestMapStruct.streamMessage.SequenceNumber] = requestMapStruct.streamMessageContent.Content
				}

				// Always attempt to process out of order messages
				outOfOrderErrorContent, ok := errorBuffer[expectedErrorSeqNumber]
				for ok {
					// Keep pulling older messages
					processErrorMessage(outOfOrderErrorContent)
					outOfOrderErrorContent, ok = errorBuffer[expectedErrorSeqNumber]
				}

			} else {
				p.logger.Errorf("unhandled stream type: %s", requestMapStruct.streamMessage.Type)
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
