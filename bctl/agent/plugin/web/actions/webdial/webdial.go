package webdial

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	webaction "bastionzero.com/bctl/v1/bzerolib/plugin/web"
	bzwebdial "bastionzero.com/bctl/v1/bzerolib/plugin/web/actions/webdial"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize = 64 * 1024
)

type WebDial struct {
	tmb       tomb.Tomb
	logger    *logger.Logger
	requestId string

	doneChan chan struct{}

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	interruptChan chan bool

	remoteHost string
	remotePort int

	requestBody []byte
}

func New(logger *logger.Logger,
	streamChan chan smsg.StreamMessage,
	doneChan chan struct{},
	remoteHost string,
	remotePort int) (*WebDial, error) {

	return &WebDial{
		logger:           logger,
		doneChan:         doneChan,
		streamOutputChan: streamChan,
		remoteHost:       remoteHost,
		remotePort:       remotePort,
		requestBody:      []byte{},
	}, nil
}

func (w *WebDial) Kill() {
	w.tmb.Killf("we've been told to stop")
	w.tmb.Wait()
}

func (w *WebDial) Receive(action string, actionPayload []byte) ([]byte, error) {
	var rerr error
	switch bzwebdial.WebDialSubAction(action) {
	case bzwebdial.WebDialStart:
		var webDialActionRequest bzwebdial.WebDialActionPayload
		if err := json.Unmarshal(actionPayload, &webDialActionRequest); err != nil {
			rerr = fmt.Errorf("malformed web dial action payload: %s", actionPayload)
		} else {
			w.start(webDialActionRequest)
			return []byte{}, nil
		}
	case bzwebdial.WebDialInput:
		var input bzwebdial.WebInputActionPayload
		if err := json.Unmarshal(actionPayload, &input); err != nil {
			rerr = fmt.Errorf("unable to unmarshal web dial input message: %s", err)
		} else {
			return []byte{}, w.handleRequest(input)
		}
	case bzwebdial.WebDialInterrupt:
		w.interruptChan <- true
		return actionPayload, nil
	default:
		rerr = fmt.Errorf("unhandled stream action: %v", action)
	}

	w.logger.Error(rerr)
	return []byte{}, rerr
}

func (w *WebDial) start(webDialActionRequest bzwebdial.WebDialActionPayload) {
	// keep track of who we're talking to
	w.requestId = webDialActionRequest.RequestId
	w.logger.Infof("Setting request id: %s", w.requestId)
	w.streamMessageVersion = webDialActionRequest.StreamMessageVersion
	w.logger.Infof("Setting stream message version: %s", w.streamMessageVersion)
}

func (w *WebDial) handleRequest(requestPayload bzwebdial.WebInputActionPayload) error {
	w.requestBody = append(w.requestBody, requestPayload.Body...)
	if requestPayload.More {
		return nil
	} else {
		w.logger.Debugf("Received request in %d part(s)", requestPayload.SequenceNumber+1)
		return w.handleNewHttpRequest(requestPayload)
	}
}

func (w *WebDial) handleNewHttpRequest(requestPayload bzwebdial.WebInputActionPayload) error {

	if request, err := w.buildHttpRequest(requestPayload.Endpoint, w.requestBody, requestPayload.Method, requestPayload.Headers); err != nil {
		return err
	} else {

		// We don't want to attempt to follow any redirect, we want to allow the browser/client to decided to
		// redirect if they choose too
		// Ref: https://stackoverflow.com/questions/23297520/how-can-i-make-the-go-http-client-not-follow-redirects-automatically
		httpClient := &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}

		if response, err := httpClient.Do(request); err != nil {
			rerr := fmt.Errorf("bad response to http request: %s", err)
			w.logger.Error(rerr)

			responsePayload := &bzwebdial.WebOutputActionPayload{
				StatusCode: http.StatusBadGateway,
				RequestId:  requestPayload.RequestId,
				Headers:    map[string][]string{},
				Content:    []byte{},
			}

			switch w.streamMessageVersion {
			// prior to 202204
			case "":
				w.sendStreamMessage(0, smsg.WebError, false, responsePayload)
			default:
				w.sendStreamMessage(0, smsg.Error, false, responsePayload)
			}
			return rerr
		} else {

			// listen and process stream messages
			w.tmb.Go(func() error {
				defer response.Body.Close()
				defer close(w.doneChan)

				// Build the header response
				header := make(map[string][]string)
				for key, value := range response.Header {
					header[key] = value
				}

				sequenceNumber := 0
				buf := make([]byte, chunkSize)
				var responsePayload *bzwebdial.WebOutputActionPayload

				for {
					select {
					case <-w.tmb.Dying():
						return nil
					default:

						// golang does the chunking for us, here. We just need to read from the body in the chunk size we want
						// "The response body is streamed on demand as the Body field is read"
						// ref: https://go.dev/src/net/http/response.go
						numBytes, err := response.Body.Read(buf)

						// check for error and if it's serious then report it
						if err != nil && err != io.EOF {
							w.logger.Errorf("error reading response body: %s", err)

							// Do not quit, just return the user the api request info
							responsePayload = &bzwebdial.WebOutputActionPayload{
								StatusCode: http.StatusBadGateway,
								RequestId:  w.requestId,
								Headers:    map[string][]string{},
								Content:    buf[:numBytes],
							}

							switch w.streamMessageVersion {
							// prior to 202204
							case "":
								w.sendStreamMessage(sequenceNumber, smsg.WebError, false, responsePayload)
							default:
								w.sendStreamMessage(sequenceNumber, smsg.Error, false, responsePayload)
							}
						}

						w.logger.Tracef("Building response for chunk #%d of size %d", sequenceNumber, numBytes)

						// Now we need to send that data back to the client
						responsePayload = &bzwebdial.WebOutputActionPayload{
							StatusCode: response.StatusCode,
							RequestId:  w.requestId,
							Headers:    header,
							Content:    buf[:numBytes],
						}

						// we get io.EOFs on whichever read call processes the final byte
						if err == io.EOF {
							// this is the final message so let the daemon know
							switch w.streamMessageVersion {
							// prior to 202204
							case "":
								w.sendStreamMessage(sequenceNumber, smsg.WebStreamEnd, false, responsePayload)
							default:
								w.sendStreamMessage(sequenceNumber, smsg.Stream, false, responsePayload)
							}
							return nil
						} else {
							switch w.streamMessageVersion {
							// prior to 202204
							case "":
								w.sendStreamMessage(sequenceNumber, smsg.WebStream, true, responsePayload)
							default:
								w.sendStreamMessage(sequenceNumber, smsg.Stream, true, responsePayload)
							}
						}

						sequenceNumber += 1
					}
				}
			})
		}
	}

	return nil
}

func (w *WebDial) sendStreamMessage(sequenceNumber int, streamType smsg.StreamType, more bool, payload *bzwebdial.WebOutputActionPayload) {
	responsePayloadBytes, _ := json.Marshal(payload)
	w.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion:  w.streamMessageVersion,
		SequenceNumber: sequenceNumber,
		Action:         string(webaction.Dial),
		Type:           streamType,
		More:           more,
		Content:        base64.StdEncoding.EncodeToString(responsePayloadBytes),
	}
}

func (w *WebDial) buildHttpRequest(endpoint string, body []byte, method string, headers map[string][]string) (*http.Request, error) {

	// Build the endpoint given the remoteHost
	remoteUrl := fmt.Sprintf("%s:%v", w.remoteHost, w.remotePort)

	if endpoint, err := bzhttp.BuildEndpoint(remoteUrl, endpoint); err != nil {
		return nil, err
	} else if remoteHostUrl, err := url.Parse(w.remoteHost); err != nil {
		w.logger.Error(fmt.Errorf("error parsing remote host url %s", w.remoteHost))
		return nil, err
	} else {
		bodyBytesReader := bytes.NewReader(body)
		req, _ := http.NewRequest(method, endpoint, bodyBytesReader)

		// Add any headers
		for name, values := range headers {

			// Loop over all values for the name.
			for _, value := range values {
				req.Header.Set(name, value)
			}
		}

		req.Header.Set("Host", remoteHostUrl.Host)
		return req, nil
	}
}
