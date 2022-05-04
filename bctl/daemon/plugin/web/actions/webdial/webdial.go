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
	tmb       tomb.Tomb
	logger    *logger.Logger
	requestId string

	// input and output channels relative to this plugin
	outboxQueue     chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage

	// plugin done channel for signalling to the datachannel we're done
	doneChan chan struct{}

	// keep track of our expected streams
	expectedSequenceNumber int
	streamMessages         map[int]smsg.StreamMessage
}

func New(logger *logger.Logger, requestId string, outboxQueue chan plugin.ActionWrapper, doneChan chan struct{}) *WebDialAction {
	return &WebDialAction{
		logger:                 logger,
		requestId:              requestId,
		outboxQueue:            outboxQueue,
		streamInputChan:        make(chan smsg.StreamMessage, 25),
		doneChan:               doneChan,
		expectedSequenceNumber: 0,
		streamMessages:         make(map[int]smsg.StreamMessage),
	}
}

func (w *WebDialAction) Kill() {
	w.tmb.Killf("we were told to die")
	w.tmb.Wait()
}

func (w *WebDialAction) Start(writer http.ResponseWriter, request *http.Request) error {
	// Send payload to plugin output queue
	payload := bzwebdial.WebDialActionPayload{
		RequestId:            w.requestId,
		StreamMessageVersion: smsg.CurrentSchema,
	}
	w.outbox(bzwebdial.WebDialStart, payload)

	// Listen to stream messages coming from bastion, and forward to our local connection
	// this signals to the parent plugin that the action is done
	defer close(w.doneChan)

	// First modify the host header to reflect what we are trying to connect to
	// Ref: https://hackernoon.com/writing-a-reverse-proxy-in-just-one-line-with-go-c1edfa78c84b
	request.Header.Set("X-Forwarded-Host", request.Host)

	// First extract the headers out of the request
	headers := bzhttp.GetHeaders(request.Header)
	headerSet := false

	// Send our request, in chunks if the body > chunksize
	w.sendRequestChunks(request.Body, request.URL.String(), headers, request.Method)

	for {
		select {
		case <-w.tmb.Dying():
			return nil
		case <-w.doneChan:
			return nil
		case <-request.Context().Done():
			w.logger.Info("HTTP request cancelled. Sending interrupt signal to agent.")

			// send the agent our interrupt message
			payload := bzwebdial.WebInterruptActionPayload{
				RequestId: w.requestId,
			}
			w.outbox(bzwebdial.WebDialInterrupt, payload)
			return fmt.Errorf("http request cancelled")
		case data := <-w.streamInputChan:
			// may have gotten an old-fashioned or newfangled message type, depending on what we asked for
			switch data.Type {
			case smsg.WebStream, smsg.WebStreamEnd, smsg.Stream:
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
						// again, might be hearing about this via old language or new
						if nextMessage.Type == smsg.WebStreamEnd || (nextMessage.Type == smsg.Stream && !nextMessage.More) {
							return nil
						}
						delete(w.streamMessages, w.expectedSequenceNumber)
						w.expectedSequenceNumber += 1
					}
				}
			default:
				w.logger.Errorf("unhandled stream type: %s", data.Type)
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

			// send the agent our interrupt message
			payload := bzwebdial.WebInterruptActionPayload{
				RequestId: w.requestId,
			}
			w.outbox(bzwebdial.WebDialInterrupt, payload)
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
		w.outbox(bzwebdial.WebDialInput, dataInPayload)

		sequenceNumber++
	}
}

func (w *WebDialAction) outbox(action bzwebdial.WebDialSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	w.outboxQueue <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: &payloadBytes,
	}
}

func (w *WebDialAction) ReceiveStream(smessage smsg.StreamMessage) {
	w.streamInputChan <- smessage
}
