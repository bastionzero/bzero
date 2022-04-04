package webdial

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzwebdial "bastionzero.com/bctl/v1/bzerolib/plugin/web/actions/webdial"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"

	"gopkg.in/tomb.v2"
)

const (
	chunkSize = 124 * 1024 // 124KB
)

type WebDialAction struct {
	logger *logger.Logger

	requestId string

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage

	// keep track of our expected streams
	expectedSequenceNumber int
	streamMessages         map[int]smsg.StreamMessage
}

func New(logger *logger.Logger,
	requestId string) (*WebDialAction, chan plugin.ActionWrapper) {

	stream := &WebDialAction{
		logger: logger,

		requestId: requestId,

		outputChan:      make(chan plugin.ActionWrapper, 50),
		streamInputChan: make(chan smsg.StreamMessage, 50),

		expectedSequenceNumber: 0,

		streamMessages: make(map[int]smsg.StreamMessage),
	}

	return stream, stream.outputChan
}

func (w *WebDialAction) Start(tmb *tomb.Tomb, writer http.ResponseWriter, request *http.Request) error {
	// Build the action payload to start the web action dial
	payload := bzwebdial.WebDialActionPayload{
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
	// First modify the host header to reflect what we are trying to connect to
	// Ref: https://hackernoon.com/writing-a-reverse-proxy-in-just-one-line-with-go-c1edfa78c84b
	request.Header.Set("X-Forwarded-Host", request.Host)

	// First extract the headers out of the request
	headers := bzhttp.GetHeaders(request.Header)

	// Send our request, in chunks if the body > chunksize
	w.sendRequestChunks(request.Body, request.URL.String(), headers, request.Method)

	// this signals to the parent plugin that the action is done
	defer close(w.outputChan)

	processInput := true
	headerSet := false

	// Listen to stream messages coming from bastion, and forward to our local connection
	for {
		select {
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

			return nil
		case data := <-w.streamInputChan:
			if !processInput {
				continue
			}
			switch smsg.SchemaVersion(data.SchemaVersion) {
			// as of 202204
			case smsg.CurrentSchema:
				// look at Type and TypeV2 -- that way, when the agent removes TypeV2, we won't break
				if smsg.StreamType(data.Type) == smsg.Stream || smsg.StreamType(data.TypeV2) == smsg.Stream {

					w.streamMessages[data.SequenceNumber] = data

					// process the incoming stream messages *in order*
					for nextMessage, ok := w.streamMessages[w.expectedSequenceNumber]; ok; nextMessage, ok = w.streamMessages[w.expectedSequenceNumber] {
						var response bzwebdial.WebOutputActionPayload
						if contentBytes, err := base64.StdEncoding.DecodeString(nextMessage.Content); err != nil {
							return err
						} else if err := json.Unmarshal(contentBytes, &response); err != nil {
							rerr := fmt.Errorf("could not unmarshal web dial output action payload: %s", err)
							w.logger.Error(rerr)
							return rerr
						} else {

							// we only write this header once
							// ref: https://stackoverflow.com/questions/57828645/how-to-handle-superfluous-response-writeheader-call-in-order-to-return-500
							if !headerSet {

								// extract and build our writer headers
								for name, values := range response.Headers {
									for _, value := range values {
										writer.Header().Add(name, value)
									}
								}

								writer.WriteHeader(response.StatusCode)
								headerSet = true
							}

							// write response to user
							w.logger.Tracef("Writing chunk #%d of size %d", w.expectedSequenceNumber, len(response.Content))
							writer.Write(response.Content)

							// if this is our last stream message, then we can return
							if !data.More {
								return nil
							}

							delete(w.streamMessages, w.expectedSequenceNumber)
							w.expectedSequenceNumber += 1
						}
					}
				} else {
					w.logger.Errorf("unhandled stream type: %s and typeV2: %s", data.Type, data.TypeV2)
				}
			// prior to 202204
			case "":
				switch smsg.StreamType(data.Type) {
				case smsg.WebStream, smsg.WebStreamEnd:
					w.streamMessages[data.SequenceNumber] = data

					// process the incoming stream messages *in order*
					for nextMessage, ok := w.streamMessages[w.expectedSequenceNumber]; ok; nextMessage, ok = w.streamMessages[w.expectedSequenceNumber] {
						var response bzwebdial.WebOutputActionPayload
						if contentBytes, err := base64.StdEncoding.DecodeString(nextMessage.Content); err != nil {
							return err
						} else if err := json.Unmarshal(contentBytes, &response); err != nil {
							rerr := fmt.Errorf("could not unmarshal web dial output action payload: %s", err)
							w.logger.Error(rerr)
							return rerr
						} else {
							// we only write this header once
							// ref: https://stackoverflow.com/questions/57828645/how-to-handle-superfluous-response-writeheader-call-in-order-to-return-500
							if !headerSet {
								// extract and build our writer headers
								for name, values := range response.Headers {
									for _, value := range values {
										writer.Header().Add(name, value)
									}
								}
								writer.WriteHeader(response.StatusCode)
								headerSet = true
							}
							// write response to user
							w.logger.Tracef("Writing chunk #%d of size %d", w.expectedSequenceNumber, len(response.Content))
							writer.Write(response.Content)

							// if this is our last stream message, then we can return
							if smsg.StreamType(nextMessage.Type) == smsg.WebStreamEnd {
								return nil
							}
						}
					}
				default:
					w.logger.Errorf("unhandled stream type: %s", data.Type)
				}
			default:
				w.logger.Errorf("unhandled schema version: %s", data.SchemaVersion)
			}

		}
	}
}

func (w *WebDialAction) sendRequestChunks(body io.ReadCloser, endpoint string, headers map[string][]string, method string) {
	buf := make([]byte, chunkSize)
	more := true
	sequenceNumber := 0

	for numBytes, err := body.Read(buf); more; numBytes, err = body.Read(buf) {
		if err == io.EOF {
			more = false
		} else if err != nil {
			w.logger.Errorf("error chunking http request: %s", err)

			returnPayload := bzwebdial.WebInterruptActionPayload{
				RequestId: w.requestId,
			}
			payloadBytes, _ := json.Marshal(returnPayload)

			// send the agent our interrupt message
			w.outputChan <- plugin.ActionWrapper{
				Action:        string(bzwebdial.WebDialInterrupt),
				ActionPayload: payloadBytes,
			}
			return
		}

		// Build the action payload
		dataInPayload := bzwebdial.WebInputActionPayload{
			RequestId:      w.requestId,
			Endpoint:       endpoint,
			Headers:        headers,
			Method:         method,
			SequenceNumber: sequenceNumber,
			Body:           buf[:numBytes],
			More:           more,
		}

		// Send payload to plugin output queue
		dataInPayloadBytes, _ := json.Marshal(dataInPayload)
		w.outputChan <- plugin.ActionWrapper{
			Action:        string(bzwebdial.WebDialInput),
			ActionPayload: dataInPayloadBytes,
		}

		sequenceNumber++
	}
}

func (w *WebDialAction) ReceiveKeysplitting(wrappedAction plugin.ActionWrapper) {
}

func (w *WebDialAction) ReceiveStream(smessage smsg.StreamMessage) {
	w.logger.Debugf("web dial action received %v stream", smessage.Type)
	w.streamInputChan <- smessage
}
