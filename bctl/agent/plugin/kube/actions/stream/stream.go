package stream

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	kubeutils "bastionzero.com/bctl/v1/bctl/agent/plugin/kube/utils"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	kubeaction "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/stream"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

type StreamAction struct {
	logger *logger.Logger
	tmb    *tomb.Tomb
	closed bool

	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion
	doneChan             chan bool

	requestId           string
	serviceAccountToken string
	kubeHost            string
	targetGroups        []string
	targetUser          string
}

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
	switch stream.StreamSubAction(action) {

	// Start exec message required before anything else
	case stream.StreamStart:
		var streamActionRequest stream.KubeStreamActionPayload
		if err := json.Unmarshal(actionPayload, &streamActionRequest); err != nil {
			rerr := fmt.Errorf("malformed Kube Stream Action payload %v", actionPayload)
			s.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		return s.StartStream(streamActionRequest, action)
	case stream.StreamStop:
		var streamActionRequest stream.KubeStreamActionPayload
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

func (s *StreamAction) StartStream(streamActionRequest stream.KubeStreamActionPayload, action string) (string, []byte, error) {
	// keep track of who we're talking to
	s.requestId = streamActionRequest.RequestId
	s.logger.Infof("Setting request id: %s", s.requestId)
	s.streamMessageVersion = streamActionRequest.StreamMessageVersion
	s.logger.Infof("Setting stream message version: %s", s.streamMessageVersion)

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
	kubeWatchHeadersPayload := stream.KubeStreamHeadersPayload{
		Headers: headers,
	}
	kubeWatchHeadersPayloadBytes, _ := json.Marshal(kubeWatchHeadersPayload)
	// Stream the response back
	s.sendStreamMessage(0, smsg.StreamData, smsg.Data, true, kubeWatchHeadersPayloadBytes[:], streamActionRequest.LogId)

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

						// If the sequenceNumber is 1, this means that we never streamed any data back, if this is a log request attempt
						// to get the latest logs
						if sequenceNumber == 1 {
							// check to see if there are any logs we can stream back, do not attempt to handle any error, this is best effort
							// Remove the follow from the endpoint
							if urlObject, err := convertToUrlObject(streamActionRequest.Endpoint); err == nil {
								// Ensure this is a log request
								if strings.HasSuffix(urlObject.Path, "/log") {
									s.handleLastLogStream(urlObject, streamActionRequest, sequenceNumber)
								}
							} else {
								s.logger.Errorf("error converting to url object: %s", err)
							}
						}
					}

					// Let the daemon know the stream has ended
					s.sendStreamMessage(sequenceNumber, smsg.StreamEnd, smsg.Stream, false, []byte{}, streamActionRequest.LogId)
					return
				}

				// Stream the response back
				s.sendStreamMessage(sequenceNumber, smsg.StreamData, smsg.Data, true, buf[:numBytes], streamActionRequest.LogId)
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

// Helper function to try and see if there are any logs we can stream to the user
// This is trigger in the case where the pod is terminated or crashing
func (s *StreamAction) handleLastLogStream(url *url.URL, streamActionRequest stream.KubeStreamActionPayload, sequenceNumber int) {
	// Remove the follow query param
	q := url.Query()
	q.Del("follow")
	url.RawQuery = q.Encode()

	// Build our http request
	if noFollowReq, err := kubeutils.BuildHttpRequest(s.kubeHost, url.String(), streamActionRequest.Body, streamActionRequest.Method, streamActionRequest.Headers, s.serviceAccountToken, s.targetUser, s.targetGroups); err == nil {
		httpClient := &http.Client{}
		if noFollowRes, err := httpClient.Do(noFollowReq); err == nil {
			// Parse out the body
			if bodyBytes, err := ioutil.ReadAll(noFollowRes.Body); err == nil {
				// Stream the context back to the user
				s.sendStreamMessage(sequenceNumber, smsg.StreamData, smsg.Data, true, bodyBytes, streamActionRequest.LogId)
			} else {
				s.logger.Errorf("error reading body of http request: %s", err)
			}
		} else {
			s.logger.Errorf("error making making http request for log endpoint: %s", err)
		}
	} else {
		s.logger.Errorf("error building log http request: %s", err)
	}
}

// Helper function to convert a string to a url object
func convertToUrlObject(inURL string) (*url.URL, error) {
	u, err := url.Parse(inURL)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *StreamAction) sendStreamMessage(
	sequenceNumber int,
	streamType smsg.StreamType,
	streamTypeV2 smsg.StreamType,
	more bool,
	toSendBytes []byte,
	logId string,
) {
	// Stream the response back
	streamMessage := smsg.StreamMessage{
		SchemaVersion:  s.streamMessageVersion,
		SequenceNumber: sequenceNumber,
		RequestId:      s.requestId,
		LogId:          logId,
		Action:         string(kubeaction.Stream),
		Type:           streamType,
		TypeV2:         streamTypeV2,
		More:           more,
		Content:        base64.StdEncoding.EncodeToString(toSendBytes),
	}
	s.streamOutputChan <- streamMessage
}
