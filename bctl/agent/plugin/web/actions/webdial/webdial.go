package webdial

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"gopkg.in/tomb.v2"

	bzwebdial "bastionzero.com/bctl/v1/bzerolib/plugin/web/actions/webdial"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize = 64 * 1024
)

type WebDial struct {
	logger    *logger.Logger
	tmb       *tomb.Tomb
	closed    bool
	requestId string

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan chan smsg.StreamMessage

	interruptChan chan bool

	remoteHost string
	remotePort int

	requestBody []byte
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
		requestBody:      []byte{},
	}, nil
}

func (w *WebDial) Closed() bool {
	return w.closed
}

func (w *WebDial) Receive(action string, actionPayload []byte) (string, []byte, error) {
	var rerr error
	switch bzwebdial.WebDialSubAction(action) {
	case bzwebdial.WebDialStart:
		var start bzwebdial.WebDialActionPayload
		if err := json.Unmarshal(actionPayload, &start); err != nil {
			rerr = fmt.Errorf("malformed web dial action payload: %s", actionPayload)
		} else {
			w.requestId = start.RequestId
			return action, []byte{}, nil
		}
	case bzwebdial.WebDialInput:
		var input bzwebdial.WebInputActionPayload
		if err := json.Unmarshal(actionPayload, &input); err != nil {
			rerr = fmt.Errorf("unable to unmarshal web dial input message: %s", err)
		} else {
			return "", []byte{}, w.handleRequest(input)
		}
	case bzwebdial.WebDialInterrupt:
		w.interruptChan <- true
		return action, actionPayload, nil
	default:
		rerr = fmt.Errorf("unhandled stream action: %v", action)
	}

	w.logger.Error(rerr)
	return "", []byte{}, rerr
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

func (w *WebDial) handleNewHttpRequest(dataIn bzwebdial.WebInputActionPayload) error {
	// Build the endpoint given the remoteHost
	remoteUrl := fmt.Sprintf("%s:%v", w.remoteHost, w.remotePort)

	if endpoint, err := bzhttp.BuildEndpoint(remoteUrl, dataIn.Endpoint); err != nil {
		return err
	} else if request, err := w.buildHttpRequest(endpoint, w.requestBody, dataIn.Method, dataIn.Headers); err != nil {
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
				RequestId:  dataIn.RequestId,
				Headers:    map[string][]string{},
				Content:    []byte{},
			}

			w.sendStreamMessage(responsePayload, 0, smsg.WebError)
			return rerr
		} else {
			go w.listenAndProcessStreamMessages(response)
		}
	}

	return nil
}

func (w *WebDial) listenAndProcessStreamMessages(response *http.Response) {
	defer response.Body.Close()

	// Build the header response
	header := make(map[string][]string)
	for key, value := range response.Header {
		header[key] = value
	}

	sequenceNumber := 0
	buf := make([]byte, chunkSize)
	var responsePayload bzwebdial.WebOutputActionPayload

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
			numBytes, err := response.Body.Read(buf)

			// check for error and if it's serious then report it
			if err != nil && err != io.EOF {
				w.logger.Errorf("error reading response body: %s", err)

				// Do not quit, just return the user the api request info
				responsePayload = bzwebdial.WebOutputActionPayload{
					StatusCode: http.StatusBadGateway,
					RequestId:  w.requestId,
					Headers:    map[string][]string{},
					Content:    buf[:numBytes],
				}

				w.sendStreamMessage(&responsePayload, sequenceNumber, smsg.WebError)
			}

			w.logger.Tracef("Building response for chunk #%d of size %d", sequenceNumber, numBytes)

			// Now we need to send that data back to the client
			responsePayload = bzwebdial.WebOutputActionPayload{
				StatusCode: response.StatusCode,
				RequestId:  w.requestId,
				Headers:    header,
				Content:    buf[:numBytes],
			}

			// if we got an io.EOF, this is the final message so let the daemon know
			streamMessage := smsg.WebStream
			if err == io.EOF {
				streamMessage = smsg.WebStreamEnd
			}

			w.sendStreamMessage(&responsePayload, sequenceNumber, streamMessage)

			// we get io.EOFs on whichever read call processes the final byte
			if err == io.EOF {
				return
			}

			sequenceNumber += 1
		}
	}
}

func (w *WebDial) sendStreamMessage(payload *bzwebdial.WebOutputActionPayload, sequenceNumber int, streamType smsg.StreamType) {
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

func (w *WebDial) buildHttpRequest(endpoint string, body []byte, method string, headers map[string][]string) (*http.Request, error) {
	bodyBytesReader := bytes.NewReader(body)
	req, _ := http.NewRequest(method, endpoint, bodyBytesReader)

	// Add any headers
	for name, values := range headers {

		// Loop over all values for the name.
		for _, value := range values {
			req.Header.Set(name, value)
		}
	}

	if remoteHostUrl, err := url.Parse(w.remoteHost); err != nil {
		w.logger.Error(fmt.Errorf("error parsing remote host url %s", w.remoteHost))
		return nil, err
	} else {
		req.Header.Set("Host", remoteHostUrl.Host)
	}

	return req, nil
}
