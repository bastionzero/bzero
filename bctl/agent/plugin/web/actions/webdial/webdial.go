package webdial

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httputil"

	"bastionzero.com/bctl/v1/bzerolib/logger"
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

	requestId     string
	remoteAddress *net.TCPAddr

	remoteConnection *tls.Conn
}

func New(logger *logger.Logger,
	pluginTmb *tomb.Tomb,
	ch chan smsg.StreamMessage,
	raddr *net.TCPAddr) (*WebDial, error) {

	return &WebDial{
		logger:           logger,
		tmb:              pluginTmb,
		closed:           false,
		streamOutputChan: ch,
		remoteAddress:    raddr,
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

		// First validate the requestId
		if err := e.validateRequestId(dataIn.RequestId); err != nil {
			return "", []byte{}, err
		}

		// Then send the data to our remote connection, decode the data first
		// dataToWrite, _ := base64.StdEncoding.DecodeString(dataIn.Data)

		// // Send this data to our remote connection
		// e.logger.Info("Received data from bastion, forwarding to remote tcp connection")
		// _, err := e.remoteConnection.Write(dataToWrite)
		// if err != nil {
		// 	e.logger.Errorf("error writing to to remote connection: %v", err)
		// 	return "", []byte{}, err
		// }

		// Now make a request to the endpoint given by the dataIn
		e.logger.Infof("Making request for %s", dataIn.Endpoint)
		req, err := BuildHttpRequest(dataIn.Endpoint, dataIn.Body, dataIn.Method, dataIn.Headers)
		if err != nil {
			return action, []byte{}, err
		}

		dump, _ := httputil.DumpRequestOut(req, true)

		e.logger.Infof("HERE: %+v", dump)

		httpClient := &http.Client{}
		res, err := httpClient.Do(req)
		if err != nil {
			rerr := fmt.Errorf("bad response to API request: %s", err)
			e.logger.Error(rerr)
			return action, []byte{}, rerr
		}
		defer res.Body.Close()

		e.logger.Infof("HERE2: %v", res.StatusCode)

		// Build the header response
		header := make(map[string][]string)
		for key, value := range res.Header {
			header[key] = value
		}

		// Parse out the body
		bodyBytes, _ := ioutil.ReadAll(res.Body)

		// Now we need to send that data back to the client
		responsePayload := WebDataOutActionPayload{
			StatusCode: res.StatusCode,
			RequestId:  dataIn.RequestId,
			Headers:    header,
			Content:    bodyBytes,
		}
		responsePayloadBytes, _ := json.Marshal(responsePayload)

		// Now send this to bastion
		str := base64.StdEncoding.EncodeToString(responsePayloadBytes)
		message := smsg.StreamMessage{
			Type:           string(smsg.WebOut),
			RequestId:      e.requestId,
			SequenceNumber: 1,
			Content:        str,
			LogId:          "", // No log id for web messages
		}
		e.streamOutputChan <- message

		return "", []byte{}, nil
	default:
		rerr := fmt.Errorf("unhandled stream action: %v", action)
		e.logger.Error(rerr)
		return "", []byte{}, rerr
	}
}

func (e *WebDial) StartDial(dialActionRequest WebDialActionPayload, action string) (string, []byte, error) {
	// Set our requestId
	e.requestId = dialActionRequest.RequestId

	// remoteConnection, err := net.DialTCP("tcp", nil, e.remoteAddress)

	// For each start, call the dial the TCP address
	conf := &tls.Config{
		// InsecureSkipVerify: true,
		// ServerName: "espn.com",
		// MinVersion: tls.VersionTLS11,
	}

	remoteConnection, err := tls.Dial("tcp", "piesocket.com:443", conf)
	if err != nil {
		e.logger.Errorf("Failed to dial remote address: %s", err)
		// Let the agent know that there was an error
		return action, []byte{}, err
	}

	// Setup a go routine to listen for messages coming from this local connection and forward to the client
	// TODO: Setup tomb for this to be cancelled?
	sequenceNumber := 1

	go func() {
		buff := make([]byte, 0xffff)
		for {
			n, err := remoteConnection.Read(buff)
			if err != nil {
				if err != io.EOF {
					e.logger.Errorf("Read failed '%s'\n", err)
				}

				// // Let our daemon know that we have got the error and we need to close the connection
				// message := smsg.StreamMessage{
				// 	Type:           string(smsg.WebAgentClose),
				// 	RequestId:      e.requestId,
				// 	SequenceNumber: sequenceNumber,
				// 	Content:        "", // No content for webAgent Close
				// 	LogId:          "", // No log id for web messages
				// }
				// e.streamOutputChan <- message

				// // Ensure that we close the dial action
				// e.closed = true
				return
			}

			tcpBytesBuffer := buff[:n]

			e.logger.Infof("Received %d bytes from local tcp connection, sending to bastion", n)

			// Now send this to bastion
			str := base64.StdEncoding.EncodeToString(tcpBytesBuffer)
			message := smsg.StreamMessage{
				Type:           string(smsg.WebOut),
				RequestId:      e.requestId,
				SequenceNumber: sequenceNumber,
				Content:        str,
				LogId:          "", // No log id for web messages
			}
			e.streamOutputChan <- message

			sequenceNumber += 1
		}
	}()

	// Update our remote connection
	e.remoteConnection = remoteConnection
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
