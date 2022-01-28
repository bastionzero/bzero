package webdial

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/utils"
	"gopkg.in/tomb.v2"

	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type WebDialSubAction string

const (
	WebDialStart  WebDialSubAction = "web/dial/start"
	WebDialDataIn WebDialSubAction = "web/dial/datain"
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
	}, nil
}

func (s *WebDial) Closed() bool {
	return s.closed
}

func (e *WebDial) Receive(action string, actionPayload []byte) (string, []byte, error) {
	switch WebDialSubAction(action) {
	case WebDialStart:
		var webDialActionRequest WebDialActionPayload
		if err := json.Unmarshal(actionPayload, &webDialActionRequest); err != nil {
			rerr := fmt.Errorf("malformed web dial Action payload %v", actionPayload)
			e.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		return e.StartDial(webDialActionRequest, action)
	case WebDialDataIn:
		// Deserialize the action payload, the only action passed is DataIn
		var dataIn WebDataInActionPayload
		if err := json.Unmarshal(actionPayload, &dataIn); err != nil {
			rerr := fmt.Errorf("unable to unmarshal dataIn message: %s", err)
			e.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		return e.HandleNewHttpRequest(action, dataIn)
	default:
		rerr := fmt.Errorf("unhandled stream action: %v", action)
		e.logger.Error(rerr)
		return "", []byte{}, rerr
	}
}

func (e *WebDial) HandleNewHttpRequest(action string, dataIn WebDataInActionPayload) (string, []byte, error) {
	// First validate the requestId
	if err := e.validateRequestId(dataIn.RequestId); err != nil {
		return "", []byte{}, err
	}

	// Build the endpoint given the remoteHost
	remoteUrl := e.remoteHost + ":" + fmt.Sprint(e.remotePort)

	endpoint, endpointErr := utils.JoinUrls(remoteUrl, dataIn.Endpoint)
	if endpointErr != nil {
		return "", []byte{}, endpointErr
	}

	// Now make a request to the endpoint given by the dataIn
	e.logger.Infof("Making request for %s", endpoint)
	req, err := BuildHttpRequest(endpoint, dataIn.Body, dataIn.Method, dataIn.Headers)
	if err != nil {
		return "", []byte{}, err
	}

	// Redefine the host header by parsing our the host from our remoteHost
	remoteHostUrl, urlParseError := url.Parse(e.remoteHost)
	if urlParseError != nil {
		e.logger.Error(fmt.Errorf("error parsing url %s", e.remoteHost))
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
	var responsePayload WebDataOutActionPayload
	if err != nil {
		rerr := fmt.Errorf("bad response to API request: %s", err)
		e.logger.Error(rerr)
		// Do not quit, just return the user the info regarding the api request
		// Now we need to send that data back to the client
		responsePayload = WebDataOutActionPayload{
			StatusCode: http.StatusInternalServerError,
			RequestId:  dataIn.RequestId,
			Headers:    map[string][]string{},
			Content:    []byte{},
		}
	} else {
		defer res.Body.Close()

		// Build the header response
		header := make(map[string][]string)
		for key, value := range res.Header {
			header[key] = value
		}

		// Parse out the body
		bodyBytes, _ := ioutil.ReadAll(res.Body)

		// Now we need to send that data back to the client
		responsePayload = WebDataOutActionPayload{
			StatusCode: res.StatusCode,
			RequestId:  dataIn.RequestId,
			Headers:    header,
			Content:    bodyBytes,
		}
	}

	responsePayloadBytes, _ := json.Marshal(responsePayload)

	// Now send this to bastion
	str := base64.StdEncoding.EncodeToString(responsePayloadBytes)
	message := smsg.StreamMessage{
		Type:           string(smsg.WebOut),
		RequestId:      e.requestId,
		SequenceNumber: 0, // Always just 1 sequence
		Content:        str,
		LogId:          "", // No log id for web messages
	}
	e.streamOutputChan <- message

	return "", []byte{}, nil
}

func (e *WebDial) StartDial(dialActionRequest WebDialActionPayload, action string) (string, []byte, error) {
	// Set our requestId
	e.requestId = dialActionRequest.RequestId

	return action, []byte{}, nil
}

func (e *WebDial) validateRequestId(requestId string) error {
	if requestId != e.requestId {
		rerr := fmt.Errorf("invalid request ID passed")
		e.logger.Error(rerr)
		return rerr
	}
	return nil
}

func BuildHttpRequest(endpoint string, body string, method string, headers map[string][]string) (*http.Request, error) {
	bodyBytesReader := bytes.NewReader([]byte(body))
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
