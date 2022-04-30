package stream

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/stream"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type StreamAction struct {
	logger *logger.Logger

	requestId       string
	logId           string
	commandBeingRun string
	doneChan        chan struct{}

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage

	expectedSequenceNumber int
	outOfOrderMessages     map[int]smsg.StreamMessage
}

func New(
	logger *logger.Logger,
	outputChan chan plugin.ActionWrapper,
	doneChan chan struct{},
	requestId string,
	logId string,
	commandBeingRun string,
) *StreamAction {

	return &StreamAction{
		logger:          logger,
		requestId:       requestId,
		logId:           logId,
		commandBeingRun: commandBeingRun,
		doneChan:        doneChan,
		outputChan:      outputChan,
		streamInputChan: make(chan smsg.StreamMessage, 10),

		// Start at 1 since we wait for our headers message
		expectedSequenceNumber: 1,
		outOfOrderMessages:     make(map[int]smsg.StreamMessage),
	}
}

func (s *StreamAction) Kill() {
	close(s.doneChan)
}

func (s *StreamAction) ReceiveKeysplitting(actionPayload []byte) {}

func (s *StreamAction) ReceiveStream(smessage smsg.StreamMessage) {
	s.logger.Debugf("Stream action received %v stream", smessage.Type)
	s.streamInputChan <- smessage
}

func (s *StreamAction) Start(writer http.ResponseWriter, request *http.Request) error {
	// First extract the headers out of the request
	headers := bzhttp.GetHeaders(request.Header)

	// Now extract the body
	bodyInBytes, err := bzhttp.GetBodyBytes(request.Body)
	if err != nil {
		s.logger.Error(err)
		return err
	}

	// Build the action payload
	payload := stream.KubeStreamActionPayload{
		Endpoint:             request.URL.String(),
		Headers:              headers,
		Method:               request.Method,
		Body:                 string(bodyInBytes), // TODO: fix this
		RequestId:            s.requestId,
		StreamMessageVersion: smsg.CurrentSchema,
		LogId:                s.logId,
		CommandBeingRun:      s.commandBeingRun,
	}

	// Send payload to plugin output queue
	s.outputChan <- plugin.ActionWrapper{
		Action:        string(stream.StreamStart),
		ActionPayload: payload,
	}

	// Wait for our initial message to determine what headers to use
	// The first message that comes from the stream is our headers message, wait for it
	// And keep any other messages that might come before
waitForHeaders:
	for {
		select {
		case <-s.doneChan:
			return nil
		case watchData := <-s.streamInputChan:
			contentBytes, _ := base64.StdEncoding.DecodeString(watchData.Content)

			// Attempt to decode contentBytes
			var kubestreamHeadersPayload stream.KubeStreamHeadersPayload
			if err := json.Unmarshal(contentBytes, &kubestreamHeadersPayload); err != nil {
				// If we see an error this must be an early message
				s.outOfOrderMessages[watchData.SequenceNumber] = watchData
			} else {
				// This is our header message, loop and apply
				for name, values := range kubestreamHeadersPayload.Headers {
					for _, value := range values {
						writer.Header().Set(name, value)
					}
				}
				break waitForHeaders
			}
		case <-time.After(30 * time.Second):
			return fmt.Errorf("timed out waiting for initial header message")
		}
	}

	// If there are any early messages, stream them first if the sequence number matches
	s.handleOutOfOrderMessage(writer)

	// Now subscribe to the response
	// Keep this as a non-go routine so we hold onto the http request
	for {
		select {
		case <-s.doneChan:
			return nil
		case <-request.Context().Done():
			s.logger.Infof("Watch request %v was cancelled", s.requestId)

			// Build the action payload
			payload := stream.KubeStreamActionPayload{
				Endpoint:  request.URL.String(),
				Headers:   headers,
				Method:    request.Method,
				Body:      string(bodyInBytes), // fix this
				RequestId: s.requestId,
				LogId:     s.logId,
			}

			s.outputChan <- plugin.ActionWrapper{
				Action:        string(stream.StreamStop),
				ActionPayload: payload,
			}

			close(s.doneChan)
		case watchData := <-s.streamInputChan:
			// may have received an old-fashioned or newfangled message, depending on what we asked for
			if watchData.Type == smsg.StreamData || watchData.Type == smsg.StreamEnd || watchData.Type == smsg.Data {
				if watchData.Type == smsg.StreamEnd || (watchData.Type == smsg.Data && !watchData.More) {
					// End the stream
					s.logger.Infof("Stream has been ended from the agent, closing request")
					close(s.doneChan)
					return nil
				}
				// Then stream the response to kubectl
				if watchData.SequenceNumber == s.expectedSequenceNumber {
					// If the incoming data is equal to the current expected seqNumber, show the user
					contentBytes, _ := base64.StdEncoding.DecodeString(watchData.Content)
					if err := kubeutils.WriteToHttpRequest(contentBytes, writer); err != nil {
						s.logger.Error(err)
						close(s.doneChan)
						return fmt.Errorf("could not write response: %s", err)
					}

					s.expectedSequenceNumber += 1
					s.handleOutOfOrderMessage(writer)
				} else {
					s.outOfOrderMessages[watchData.SequenceNumber] = watchData
				}
			} else {
				s.logger.Errorf("unhandled stream type: %s", watchData.Type)
			}

		}
	}
}

func (s *StreamAction) handleOutOfOrderMessage(writer http.ResponseWriter) {
	for outOfOrderMessageData, ok := s.outOfOrderMessages[s.expectedSequenceNumber]; ok; outOfOrderMessageData, ok = s.outOfOrderMessages[s.expectedSequenceNumber] {
		// If we have an early message, show it to the user
		contentBytes, _ := base64.StdEncoding.DecodeString(outOfOrderMessageData.Content)
		if err := kubeutils.WriteToHttpRequest(contentBytes, writer); err != nil {
			return
		}

		// Increment the seqNumber and keep looking for more
		s.expectedSequenceNumber += 1
	}
}
