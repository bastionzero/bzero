package webdial

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"bastionzero.com/bctl/v1/bctl/agent/plugin/web/actions/webdial"
	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"

	"gopkg.in/tomb.v2"
)

const (
	startDial  = "web/dial/start"
	dialDataIn = "web/dial/datain"
)

type WebDialAction struct {
	logger *logger.Logger

	requestId string

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage

	sequenceNumber int
}

func New(logger *logger.Logger,
	requestId string) (*WebDialAction, chan plugin.ActionWrapper) {

	stream := &WebDialAction{
		logger: logger,

		requestId: requestId,

		outputChan:      make(chan plugin.ActionWrapper, 10),
		streamInputChan: make(chan smsg.StreamMessage, 10),

		sequenceNumber: 0,
	}

	return stream, stream.outputChan
}

func (s *WebDialAction) Start(tmb *tomb.Tomb, Writer http.ResponseWriter, Request *http.Request) error {
	// this action ends at the end of this function, in order to signal that to the parent plugin,
	// we close the output channel which will close the go routine listening on it
	defer close(s.outputChan)

	// Build the action payload to start the web action dial
	payload := webdial.WebDialActionPayload{
		RequestId: s.requestId,
	}

	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	s.outputChan <- plugin.ActionWrapper{
		Action:        startDial,
		ActionPayload: payloadBytes,
	}

	return s.handleHttpRequest(Writer, Request)
}

func (s *WebDialAction) handleHttpRequest(writer http.ResponseWriter, request *http.Request) error {
	// First modify the host header to reflect what we are trying to connect too
	// Ref: https://hackernoon.com/writing-a-reverse-proxy-in-just-one-line-with-go-c1edfa78c84b
	request.Header.Set("X-Forwarded-Host", request.Host)

	// First extract the headers out of the request
	headers := bzhttp.GetHeaders(request.Header)

	// Now extract the body
	bodyInBytes, err := bzhttp.GetBodyBytes(request.Body)
	if err != nil {
		s.logger.Error(err)
		return err
	}

	// Build the action payload
	dataInPayload := webdial.WebDataInActionPayload{
		RequestId:      s.requestId,
		SequenceNumber: s.sequenceNumber,
		Endpoint:       request.URL.String(),
		Headers:        headers,
		Method:         request.Method,
		Body:           string(bodyInBytes), // fix this
	}

	// Send payload to plugin output queue
	dataInPayloadBytes, _ := json.Marshal(dataInPayload)
	s.outputChan <- plugin.ActionWrapper{
		Action:        dialDataIn,
		ActionPayload: dataInPayloadBytes,
	}

	// Listen to stream messages coming from bastion, and forward to our local connection
	for {
		select {
		case data := <-s.streamInputChan:
			switch smsg.StreamType(data.Type) {
			case smsg.WebOut:
				contentBytes, base64Err := base64.StdEncoding.DecodeString(data.Content)
				if base64Err != nil {
					return base64Err
				}

				var response webdial.WebDataOutActionPayload
				if err := json.Unmarshal(contentBytes, &response); err != nil {
					rerr := fmt.Errorf("could not unmarshal Action Response Payload: %s", err)
					s.logger.Error(rerr)
					return err
				}

				// extract and build our writer headers
				for name, values := range response.Headers {
					for _, value := range values {
						writer.Header().Add(name, value)
					}
				}

				// write response to user
				writer.WriteHeader(response.StatusCode)
				writer.Write(response.Content)

				return nil
			default:
				s.logger.Errorf("unhandled stream type: %s", data.Type)
			}
		}
	}
}

func (s *WebDialAction) ReceiveKeysplitting(wrappedAction plugin.ActionWrapper) {
	// We dont get any keysplitting messages
}

func (s *WebDialAction) ReceiveStream(smessage smsg.StreamMessage) {
	s.logger.Debugf("Stream action received %v stream", smessage.Type)
	s.streamInputChan <- smessage
}
