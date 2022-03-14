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
	bzwebdial "bastionzero.com/bctl/v1/bzerolib/plugin/web/actions/webdial"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"

	"gopkg.in/tomb.v2"
)

type WebDialAction struct {
	logger *logger.Logger

	requestId string

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage

	// done chan to return our main function once our interrupt is acknowledged
	doneChan chan bool
}

func New(logger *logger.Logger,
	requestId string) (*WebDialAction, chan plugin.ActionWrapper) {

	stream := &WebDialAction{
		logger: logger,

		requestId: requestId,

		outputChan:      make(chan plugin.ActionWrapper, 50),
		streamInputChan: make(chan smsg.StreamMessage, 50),

		doneChan: make(chan bool),
	}

	return stream, stream.outputChan
}

func (w *WebDialAction) Start(tmb *tomb.Tomb, writer http.ResponseWriter, request *http.Request) error {
	// Build the action payload to start the web action dial
	payload := webdial.WebDialActionPayload{
		RequestId: w.requestId,
	}

	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	w.outputChan <- plugin.ActionWrapper{
		Action:        string(bzwebdial.WebDialStart),
		ActionPayload: payloadBytes,
	}

	return w.handleHttpRequest(writer, request)
}

func (w *WebDialAction) handleHttpRequest(writer http.ResponseWriter, request *http.Request) error {
	// First modify the host header to reflect what we are trying to connect too
	// Ref: https://hackernoon.com/writing-a-reverse-proxy-in-just-one-line-with-go-c1edfa78c84b
	request.Header.Set("X-Forwarded-Host", request.Host)

	// First extract the headers out of the request
	headers := bzhttp.GetHeaders(request.Header)

	// Now extract the body
	bodyInBytes, err := bzhttp.GetBodyBytes(request.Body)
	if err != nil {
		w.logger.Error(err)
		return err
	}

	// Build the action payload
	dataInPayload := webdial.WebInputActionPayload{
		RequestId:      w.requestId,
		SequenceNumber: 0, // currently do not implement sequence number
		Endpoint:       request.URL.String(),
		Headers:        headers,
		Method:         request.Method,
		Body:           bodyInBytes,
	}

	// Send payload to plugin output queue
	dataInPayloadBytes, _ := json.Marshal(dataInPayload)
	w.outputChan <- plugin.ActionWrapper{
		Action:        string(bzwebdial.WebDialInput),
		ActionPayload: dataInPayloadBytes,
	}

	processInput := true
	headerSet := false
	// Listen to stream messages coming from bastion, and forward to our local connection
	for {
		// this signals to the parent plugin that the action is done
		defer close(w.outputChan)

		select {
		case <-w.doneChan:
			return nil
		case <-request.Context().Done():
			// only send one interrupt message to the agent
			if !processInput {
				continue
			}

			w.logger.Info("HTTP request cancelled. Sending interrupt signal to agent.")

			returnPayload := bzwebdial.WebInterruptActionPayload{
				RequestId: w.requestId,
			}
			payloadBytes, _ := json.Marshal(returnPayload)

			// send the agent our interrupt message
			w.outputChan <- plugin.ActionWrapper{
				Action:        string(bzwebdial.WebDialInterrupt),
				ActionPayload: payloadBytes,
			}

			// now that we've recieved the interrupt, we should process any more stream message from the agent
			// but the agent has been sending us messages in the meantime and we need to quietly consume and ignore those
			// that's why we stop processing input (processInput = false) and only return once we recieve the ack
			// for our interrupt message
			processInput = false
		case data := <-w.streamInputChan:
			if !processInput {
				continue
			}

			switch smsg.StreamType(data.Type) {
			case smsg.WebStream, smsg.WebStreamEnd:
				contentBytes, base64Err := base64.StdEncoding.DecodeString(data.Content)
				if base64Err != nil {
					return base64Err
				}

				var response webdial.WebOutputActionPayload
				if err := json.Unmarshal(contentBytes, &response); err != nil {
					rerr := fmt.Errorf("could not unmarshal web dial output action payload: %s", err)
					w.logger.Error(rerr)
					return err
				}

				// extract and build our writer headers
				for name, values := range response.Headers {
					for _, value := range values {
						writer.Header().Add(name, value)
					}
				}

				// we should only set this header once
				// ref: https://stackoverflow.com/questions/57828645/how-to-handle-superfluous-response-writeheader-call-in-order-to-return-500
				if !headerSet {
					writer.WriteHeader(response.StatusCode)
					headerSet = true
				}

				// write response to user
				writer.Write(response.Content)

				// if this is our last stream message, then we can return
				if smsg.StreamType(data.Type) == smsg.WebStreamEnd {
					return nil
				}
			default:
				w.logger.Errorf("unhandled stream type: %s", data.Type)
			}
		}
	}
}

func (w *WebDialAction) ReceiveKeysplitting(wrappedAction plugin.ActionWrapper) {
	if wrappedAction.Action == string(bzwebdial.WebDialInterrupt) {
		w.doneChan <- true
	}
}

func (w *WebDialAction) ReceiveStream(smessage smsg.StreamMessage) {
	w.logger.Debugf("web dial action received %v stream", smessage.Type)
	w.streamInputChan <- smessage
}
