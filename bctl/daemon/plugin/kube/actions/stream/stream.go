package stream

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"gopkg.in/tomb.v2"

	kubestream "bastionzero.com/bctl/v1/bctl/agent/plugin/kube/actions/stream"
	kubeutils "bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/utils"
	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type StreamAction struct {
	logger *logger.Logger

	requestId       string
	logId           string
	commandBeingRun string

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage
	ksInputChan     chan plugin.ActionWrapper

	expectedSequenceNumber int
	outOfOrderMessages     map[int]smsg.StreamMessage
	writer                 http.ResponseWriter
}

func New(logger *logger.Logger,
	requestId string,
	logId string,
	commandBeingRun string) (*StreamAction, chan plugin.ActionWrapper) {

	stream := &StreamAction{
		logger: logger,

		requestId:       requestId,
		logId:           logId,
		commandBeingRun: commandBeingRun,

		outputChan:      make(chan plugin.ActionWrapper, 10),
		streamInputChan: make(chan smsg.StreamMessage, 10),
		ksInputChan:     make(chan plugin.ActionWrapper, 10),

		// Start at 1 since we wait for our headers message
		expectedSequenceNumber: 1,
		outOfOrderMessages:     make(map[int]smsg.StreamMessage),
	}

	return stream, stream.outputChan
}

func (s *StreamAction) ReceiveKeysplitting(wrappedAction plugin.ActionWrapper) {
	s.ksInputChan <- wrappedAction
}

func (s *StreamAction) ReceiveStream(smessage smsg.StreamMessage) {
	s.logger.Debugf("Stream action received %v stream", smessage.Type)
	s.streamInputChan <- smessage
}

func (s *StreamAction) Start(tmb *tomb.Tomb, writer http.ResponseWriter, request *http.Request) error {
	// this action ends at the end of this function, in order to signal that to the parent plugin,
	// we close the output channel which will close the go routine listening on it
	defer close(s.outputChan)

	// Set our writer
	s.writer = writer

	// First extract the headers out of the request
	headers := bzhttp.GetHeaders(request.Header)

	// Now extract the body
	bodyInBytes, err := bzhttp.GetBodyBytes(request.Body)
	if err != nil {
		s.logger.Error(err)
		return err
	}

	// Build the action payload
	payload := kubestream.KubeStreamActionPayload{
		Endpoint:        request.URL.String(),
		Headers:         headers,
		Method:          request.Method,
		Body:            string(bodyInBytes), // fix this
		RequestId:       s.requestId,
		LogId:           s.logId,
		CommandBeingRun: s.commandBeingRun,
	}

	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	s.outputChan <- plugin.ActionWrapper{
		Action:        string(kubestream.StreamStart),
		ActionPayload: payloadBytes,
	}

	// Wait for our initial message to determine what headers to use
	// The first message that comes from the stream is our headers message, wait for it
	// And keep any other messages that might come before
outOfOrderMessageHandler:
	for {
		select {
		case <-tmb.Dying():
			return nil
		case watchData := <-s.streamInputChan:

			contentBytes, _ := base64.StdEncoding.DecodeString(watchData.Content)

			// Attempt to decode contentBytes
			var kubestreamHeadersPayload kubestream.KubeStreamHeadersPayload
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
				break outOfOrderMessageHandler
			}
		}
	}

	// If there are any early messages, stream them first if the sequence number matches
	s.handleOutOfOrderMessage()

	// Now subscribe to the response
	// Keep this as a non-go routine so we hold onto the http request
	for {
		select {
		case <-tmb.Dying():
			return nil
		case <-request.Context().Done():
			s.logger.Infof("Watch request %v was cancelled", s.requestId)

			// Build the action payload
			payload := kubestream.KubeStreamActionPayload{
				Endpoint:  request.URL.String(),
				Headers:   headers,
				Method:    request.Method,
				Body:      string(bodyInBytes), // fix this
				RequestId: s.requestId,
				LogId:     s.logId,
			}

			payloadBytes, _ := json.Marshal(payload)
			s.outputChan <- plugin.ActionWrapper{
				Action:        string(kubestream.StreamStop),
				ActionPayload: payloadBytes,
			}

			return nil

		case watchData := <-s.streamInputChan:

			switch watchData.SchemaVersion {
			// as of 202204
			case smsg.CurrentSchema:
				if watchData.Type == smsg.Data || watchData.TypeV2 == smsg.Data {
					if !watchData.More {
						// End the stream
						s.logger.Infof("Stream has been ended from the agent, closing request")
						return nil
					}
					// Then stream the response to kubectl
					if watchData.SequenceNumber == s.expectedSequenceNumber {
						// If the incoming data is equal to the current expected seqNumber, show the user
						contentBytes, _ := base64.StdEncoding.DecodeString(watchData.Content)
						if err := kubeutils.WriteToHttpRequest(contentBytes, writer); err != nil {
							s.logger.Error(err)
							return nil
						}

						// Increment the seqNumber
						s.expectedSequenceNumber += 1

						// See if we have any early messages for this seqNumber
						s.handleOutOfOrderMessage()
					} else {
						s.outOfOrderMessages[watchData.SequenceNumber] = watchData
					}
				} else {
					s.logger.Errorf("unhandled stream type: %s and typeV2: %s", watchData.Type, watchData.TypeV2)
				}
			// prior to 202204
			case "":
				// Determine if this is an end or data messages
				switch watchData.Type {
				case smsg.StreamData:
					// Then stream the response to kubectl
					if watchData.SequenceNumber == s.expectedSequenceNumber {
						// If the incoming data is equal to the current expected seqNumber, show the user
						contentBytes, _ := base64.StdEncoding.DecodeString(watchData.Content)
						if err := kubeutils.WriteToHttpRequest(contentBytes, writer); err != nil {
							s.logger.Error(err)
							return nil
						}
						// Increment the seqNumber
						s.expectedSequenceNumber += 1
						// See if we have any early messages for this seqNumber
						s.handleOutOfOrderMessage()
					} else {
						s.outOfOrderMessages[watchData.SequenceNumber] = watchData
					}
				case smsg.StreamEnd:
					// End the stream
					s.logger.Infof("Stream has been ended from the agent, closing request")
					return nil
				default:
					s.logger.Errorf("unhandled stream message: %s", watchData.Type)
				}
			default:
				s.logger.Errorf("unhandled schema version: %s", watchData.SchemaVersion)
			}
		}
	}
}

func (s *StreamAction) handleOutOfOrderMessage() {
	outOfOrderMessageData, ok := s.outOfOrderMessages[s.expectedSequenceNumber]
	for ok {
		// If we have an early message, show it to the user
		contentBytes, _ := base64.StdEncoding.DecodeString(outOfOrderMessageData.Content)
		err := kubeutils.WriteToHttpRequest(contentBytes, s.writer)
		if err != nil {
			return
		}

		// Increment the seqNumber and keep looking for more
		s.expectedSequenceNumber += 1
		outOfOrderMessageData, ok = s.outOfOrderMessages[s.expectedSequenceNumber]
	}
}
