package stream

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	kubeutils "bastionzero.com/bctl/v1/bctl/agent/plugin/kube/utils"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

type StreamAction struct {
	logger *logger.Logger
	tmb    *tomb.Tomb
	closed bool

	streamOutputChan chan smsg.StreamMessage
	doneChan         chan bool

	requestId           string
	serviceAccountToken string
	kubeHost            string
	targetGroups        []string
	targetUser          string
}

type StreamSubAction string

const (
	StreamData  StreamSubAction = "kube/stream/stdout"
	StreamStart StreamSubAction = "kube/stream/start"
	StreamStop  StreamSubAction = "kube/stream/stop"
)

func New(logger *logger.Logger,
	pluginTmb *tomb.Tomb,
	serviceAccountToken string,
	kubeHost string,
	targetGroups []string,
	targetUser string,
	ch chan smsg.StreamMessage) (*StreamAction, error) {
	return &StreamAction{
		logger:              logger,
		tmb:                 pluginTmb,
		closed:              false,
		streamOutputChan:    ch,
		doneChan:            make(chan bool),
		serviceAccountToken: serviceAccountToken,
		kubeHost:            kubeHost,
		targetGroups:        targetGroups,
		targetUser:          targetUser,
	}, nil
}

func (s *StreamAction) Closed() bool {
	return s.closed
}

func (s *StreamAction) Receive(action string, actionPayload []byte) (string, []byte, error) {
	switch StreamSubAction(action) {

	// Start exec message required before anything else
	case StreamStart:
		var streamActionRequest KubeStreamActionPayload
		if err := json.Unmarshal(actionPayload, &streamActionRequest); err != nil {
			rerr := fmt.Errorf("malformed Kube Stream Action payload %v", actionPayload)
			s.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		s.requestId = streamActionRequest.RequestId

		return s.StartStream(streamActionRequest, action)
	case StreamStop:
		var streamActionRequest KubeStreamActionPayload
		if err := json.Unmarshal(actionPayload, &streamActionRequest); err != nil {
			rerr := fmt.Errorf("malformed Kube Stream Action payload %v", actionPayload)
			s.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		// check requestid matches
		if err := kubeutils.MatchRequestId(streamActionRequest.RequestId, s.requestId); err != nil {
			s.logger.Error(err)
			return "", []byte{}, err
		}

		s.logger.Info("Stopping Stream Action")
		s.doneChan <- true // close the go routines
		s.closed = true

		return "", []byte{}, nil
	default:
		rerr := fmt.Errorf("unhandled stream action: %v", action)
		s.logger.Error(rerr)
		return "", []byte{}, rerr
	}
}

func (s *StreamAction) StartStream(streamActionRequest KubeStreamActionPayload, action string) (string, []byte, error) {
	// Build our request
	s.logger.Infof("Making request for %s", streamActionRequest.Endpoint)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := s.buildHttpRequest(streamActionRequest.Endpoint, streamActionRequest.Body, streamActionRequest.Method, streamActionRequest.Headers)
	if err != nil {
		s.logger.Error(err)
		return action, []byte{}, err
	}

	// Make the request and wait for the body to close
	req = req.WithContext(ctx)
	httpClient := &http.Client{}
	res, err := httpClient.Do(req)
	if err != nil {
		rerr := fmt.Errorf("bad response to API request: %s", err)
		s.logger.Error(rerr)
		return action, []byte{}, rerr
	}

	// Send our first message with the headers
	headers := make(map[string][]string)
	for name, value := range res.Header {
		headers[name] = value
	}
	kubeWatchHeadersPayload := KubeStreamHeadersPayload{
		Headers: headers,
	}
	kubeWatchHeadersPayloadBytes, _ := json.Marshal(kubeWatchHeadersPayload)
	content := base64.StdEncoding.EncodeToString(kubeWatchHeadersPayloadBytes[:])

	// Stream the response back
	message := smsg.StreamMessage{
		Type:           string(StreamData),
		RequestId:      streamActionRequest.RequestId,
		LogId:          streamActionRequest.LogId,
		SequenceNumber: 0,
		Content:        content,
	}
	s.streamOutputChan <- message

	// Create our bufio object
	buf := make([]byte, 1024)
	br := bufio.NewReader(res.Body)

	sequenceNumber := 1

	go func() {
		defer res.Body.Close()
		for {
			select {
			case <-s.tmb.Dying():
				return
			default:
				// Read into the buffer
				numBytes, err := io.ReadFull(br, buf)

				if err != nil {
					switch err {
					case context.Canceled:
						s.logger.Info("Stream action stream closed")
					case io.EOF:
						s.logger.Info("Received EOF on stream action stream")
					default:
						s.logger.Error(fmt.Errorf("could not read HTTP response: %s", err))
					}
					return
				}

				// Stream the response back
				content := base64.StdEncoding.EncodeToString(buf[:numBytes])
				message := smsg.StreamMessage{
					Type:           string(StreamData),
					RequestId:      streamActionRequest.RequestId,
					LogId:          streamActionRequest.LogId,
					SequenceNumber: sequenceNumber,
					Content:        content,
				}

				s.streamOutputChan <- message
				sequenceNumber += 1
			}
		}
	}()

	// Subscribe to our done channel
	go func() {
		defer cancel()
		for {
			select {
			case <-s.tmb.Dying():
				return
			case <-s.doneChan:
				return
			}
		}
	}()

	return action, []byte{}, nil
}

func (s *StreamAction) buildHttpRequest(endpoint, body, method string, headers map[string][]string) (*http.Request, error) {
	if toReturn, err := kubeutils.BuildHttpRequest(s.kubeHost, endpoint, body, method, headers, s.serviceAccountToken, s.targetUser, s.targetGroups); err != nil {
		return nil, err
	} else {
		return toReturn, nil
	}
}
