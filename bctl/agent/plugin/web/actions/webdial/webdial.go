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
		var start WebDialActionPayload
		if err := json.Unmarshal(actionPayload, &start); err != nil {
			rerr = fmt.Errorf("malformed web dial action payload: %s", actionPayload)
		} else {
			// Set our requestId
			w.requestId = start.RequestId
			return action, []byte{}, nil
		}
	case bzwebdial.WebDialInput:
		var input WebInputActionPayload
		if err := json.Unmarshal(actionPayload, &input); err != nil {
			rerr = fmt.Errorf("unable to unmarshal web dial input message: %s", err)
		} else {
			return w.handleNewHttpRequest(action, input)
		}
	case bzwebdial.WebDialInterrupt:
		w.interruptChan <- true
		return action, actionPayload, nil
	default:
		rerr = fmt.Errorf("unhandled stream action: %v", action)
	}

	// if we've gotten here, we've hit an error
	w.logger.Error(rerr)
	return "", []byte{}, rerr
}

func (w *WebDial) handleNewHttpRequest(action string, dataIn WebInputActionPayload) (string, []byte, error) {
	// Build the endpoint given the remoteHost
	remoteUrl := fmt.Sprintf("%s:%v", w.remoteHost, w.remotePort)

	if endpoint, err := bzhttp.BuildEndpoint(remoteUrl, dataIn.Endpoint); err != nil {
		return "", []byte{}, err
	} else if request, err := buildHttpRequest(endpoint, dataIn.Body, dataIn.Method, dataIn.Headers); err != nil {
		return "", []byte{}, err
	} else if remoteHostUrl, err := url.Parse(w.remoteHost); err != nil {
		w.logger.Error(fmt.Errorf("error parsing remote host url %s", w.remoteHost))
		return "", []byte{}, err
	} else {
		request.Header.Set("Host", remoteHostUrl.Host)

		// We don't want to attempt to follow any redirect, we want to allow the browser/client to decided to
		// redirect if they choose too
		// Ref: https://stackoverflow.com/questions/23297520/how-can-i-make-the-go-http-client-not-follow-redirects-automatically
		httpClient := &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}

		if response, err := httpClient.Do(request); err != nil {
			rerr := fmt.Errorf("bad response to API request: %s", err)
			w.logger.Error(rerr)

			// Do not quit, just return the user the info regarding the api request
			responsePayload := &WebOutputActionPayload{
				StatusCode: http.StatusBadGateway,
				RequestId:  dataIn.RequestId,
				Headers:    map[string][]string{},
				Content:    []byte{},
			}

			w.sendStreamMessage(0, smsg.Error, false, responsePayload)
			return "", []byte{}, rerr
		} else {
			go w.listenAndProcessStreamMessages(response)
		}
	}

	return "", []byte{}, nil
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
	var responsePayload *WebOutputActionPayload

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
				responsePayload = &WebOutputActionPayload{
					StatusCode: http.StatusBadGateway,
					RequestId:  w.requestId,
					Headers:    map[string][]string{},
					Content:    buf[:numBytes],
				}

				w.sendStreamMessage(sequenceNumber, smsg.Error, false, responsePayload)
			}

			w.logger.Tracef("Building response for chunk #%d of size %d", sequenceNumber, numBytes)

			// Now we need to send that data back to the client
			responsePayload = &WebOutputActionPayload{
				StatusCode: response.StatusCode,
				RequestId:  w.requestId,
				Headers:    header,
				Content:    buf[:numBytes],
			}

			// we get io.EOFs on whichever read call processes the final byte
			if err == io.EOF {
				// this is the final message so let the daemon know
				w.sendStreamMessage(sequenceNumber, smsg.Stream, false, responsePayload)
				return
			} else {
				w.sendStreamMessage(sequenceNumber, smsg.Stream, true, responsePayload)
			}

			sequenceNumber += 1
		}
	}
}

func (w *WebDial) sendStreamMessage(sequenceNumber int, streamType smsg.StreamType, more bool, payload *WebOutputActionPayload) {
	responsePayloadBytes, _ := json.Marshal(payload)
	payloadStr := base64.StdEncoding.EncodeToString(responsePayloadBytes)
	message := smsg.StreamMessage{
		SchemaVersion:  smsg.CurrentSchema,
		SequenceNumber: sequenceNumber,
		Action:         string(webaction.Dial),
		Type:           streamType,
		More:           more,
		Content:        payloadStr,
	}
	w.streamOutputChan <- message
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
