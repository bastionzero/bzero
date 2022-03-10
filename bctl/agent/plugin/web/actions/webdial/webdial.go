package webdial

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"gopkg.in/tomb.v2"

	bzwebdial "bastionzero.com/bctl/v1/bzerolib/plugin/web/actions/webdial"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize = 32 * 1024
)

type WebDial struct {
	logger *logger.Logger
	tmb    *tomb.Tomb
	closed bool

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan chan smsg.StreamMessage

	remoteHost string
	remotePort int

	requestId string

	interruptChan chan bool
}

func New(logger *logger.Logger,
	remoteHost string,
	remotePort int,
	pluginTmb *tomb.Tomb,
	ch chan smsg.StreamMessage) (*WebDial, error) {

	return &WebDial{
		logger:           logger,
		tmb:              pluginTmb,
		closed:           false,
		streamOutputChan: ch,
		remoteHost:       remoteHost,
		remotePort:       remotePort,
		interruptChan:    make(chan bool),
	}, nil
}

func (w *WebDial) Closed() bool {
	return w.closed
}

func (w *WebDial) Receive(action string, actionPayload []byte) (string, []byte, error) {
	var rerr error

	switch bzwebdial.WebDialSubAction(action) {
	case bzwebdial.WebDialStart:
		var webDialActionRequest WebDialActionPayload
		if err := json.Unmarshal(actionPayload, &webDialActionRequest); err != nil {
			rerr := fmt.Errorf("malformed web dial action payload: %s", actionPayload)
			w.logger.Error(rerr)
		} else {
			return w.startDial(webDialActionRequest, action)
		}
	case bzwebdial.WebDialInput:
		var dataIn WebInputActionPayload
		if err := json.Unmarshal(actionPayload, &dataIn); err != nil {
			rerr := fmt.Errorf("unable to unmarshal web dial input message: %s", err)
			w.logger.Error(rerr)
		} else {
			return w.HandleNewHttpRequest(action, dataIn)
		}
	case bzwebdial.WebDialInterrupt:
		w.interruptChan <- true

		// give our streamoutputchan time to process all the messages we sent while the interrupt was getting here
		time.Sleep(2 * time.Second)

		// this return payload tells the daemon to close the action on their side
		returnPayload := bzwebdial.WebInterruptActionPayload{
			RequestId: w.requestId,
		}
		responsePayloadBytes, _ := json.Marshal(returnPayload)
		return action, responsePayloadBytes, nil
	default:
		rerr := fmt.Errorf("unhandled stream action: %v", action)
		w.logger.Error(rerr)
	}

	return "", []byte{}, rerr
}

func (w *WebDial) HandleNewHttpRequest(action string, dataIn WebInputActionPayload) (string, []byte, error) {
	// First validate the requestId
	if err := w.validateRequestId(dataIn.RequestId); err != nil {
		return "", []byte{}, err
	}

	// Build the endpoint given the remoteHost
	remoteUrl := fmt.Sprintf("%s:%v", w.remoteHost, w.remotePort)

	endpoint, endpointErr := bzhttp.BuildEndpoint(remoteUrl, dataIn.Endpoint)
	if endpointErr != nil {
		return "", []byte{}, endpointErr
	}

	// Now make a request to the endpoint given by the dataIn
	w.logger.Infof("Making request for %s", endpoint)
	req, err := buildHttpRequest(endpoint, dataIn.Body, dataIn.Method, dataIn.Headers)
	if err != nil {
		return "", []byte{}, err
	}

	// Redefine the host header by parsing our the host from our remoteHost
	remoteHostUrl, urlParseError := url.Parse(w.remoteHost)
	if urlParseError != nil {
		w.logger.Error(fmt.Errorf("error parsing url %s", w.remoteHost))
		return "", []byte{}, err
	}
	req.Header.Set("Host", remoteHostUrl.Host)

	// We don't want to attempt to follow any redirect, we want to allow the browser/client to decided to
	// redirect if they choose too
	// Ref: https://stackoverflow.com/questions/23297520/how-can-i-make-the-go-http-client-not-follow-redirects-automatically
	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := httpClient.Do(req)
	var responsePayload WebOutputActionPayload
	if err != nil {
		rerr := fmt.Errorf("bad response to API request: %s", err)
		w.logger.Error(rerr)

		// Do not quit, just return the user the info regarding the api request
		responsePayload = WebOutputActionPayload{
			StatusCode: http.StatusBadGateway,
			RequestId:  dataIn.RequestId,
			Headers:    map[string][]string{},
			Content:    []byte{},
		}
	} else {
		// Build the header response
		header := make(map[string][]string)
		for key, value := range res.Header {
			header[key] = value
		}

		// Make a routine to read the response body and send chunks to the daemon
		go func() {
			defer res.Body.Close()

			sequenceNumber := 0
			buf := make([]byte, chunkSize)

			for {
				select {
				case <-w.tmb.Dying():
					return
				case <-w.interruptChan:
					return
				default:
					// golang does the chunking for us, here. We just need to read from the body in the chunk size we want
					// "The response body is streamed on demand as the Body field is read"
					// ref: https://go.dev/src/net/http/response.go
					numBytes, err := res.Body.Read(buf)

					// check for error and if it's serious then report it
					if err != nil && err != io.EOF {
						w.logger.Errorf("error reading response body: %s", err)

						// Do not quit, just return the user the api request info
						responsePayload = WebOutputActionPayload{
							StatusCode: http.StatusBadGateway,
							RequestId:  dataIn.RequestId,
							Headers:    map[string][]string{},
							Content:    []byte{},
						}

						w.sendWebDataStreamMessage(&responsePayload, sequenceNumber, smsg.WebError)
					}

					w.logger.Debugf("Building response for chunk #%d of size %d", sequenceNumber, numBytes)

					// Now we need to send that data back to the client
					responsePayload = WebOutputActionPayload{
						StatusCode: res.StatusCode,
						RequestId:  dataIn.RequestId,
						Headers:    header,
						Content:    buf[:numBytes],
					}

					// if we got an io.EOF, this is the final message so let the daemon know
					streamMessage := smsg.WebStream
					if err == io.EOF {
						streamMessage = smsg.WebStreamEnd
					}

					w.sendWebDataStreamMessage(&responsePayload, sequenceNumber, streamMessage)
				}

				// we get io.EOFs on whichever read call processes the final byte
				if err == io.EOF {
					break
				}

				sequenceNumber += 1
			}
		}()
	}

	return "", []byte{}, nil
}

func (w *WebDial) sendWebDataStreamMessage(payload *WebOutputActionPayload, sequenceNumber int, streamType smsg.StreamType) {
	responsePayloadBytes, _ := json.Marshal(payload)
	str := base64.StdEncoding.EncodeToString(responsePayloadBytes)
	message := smsg.StreamMessage{
		Type:           string(streamType),
		RequestId:      w.requestId,
		SequenceNumber: sequenceNumber,
		Content:        str,
		LogId:          "", // No log id for web messages
	}
	w.streamOutputChan <- message
}

func (w *WebDial) startDial(dialActionRequest WebDialActionPayload, action string) (string, []byte, error) {
	// Set our requestId
	w.requestId = dialActionRequest.RequestId

	return action, []byte{}, nil
}

func (w *WebDial) validateRequestId(requestId string) error {
	if requestId != w.requestId {
		rerr := fmt.Errorf("invalid request ID passed")
		w.logger.Error(rerr)
		return rerr
	}
	return nil
}

func buildHttpRequest(endpoint string, body []byte, method string, headers map[string][]string) (*http.Request, error) {
	bodyBytesReader := bytes.NewReader(body)
	req, _ := http.NewRequest(method, endpoint, bodyBytesReader)

	// Add any headers
	for name, values := range headers {
		// Loop over all values for the name.
		for _, value := range values {
			req.Header.Set(name, value)
		}

	}

	return req, nil
}
