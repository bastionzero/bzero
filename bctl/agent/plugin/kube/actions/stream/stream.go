package stream

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	kubeaction "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/stream"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type StreamAction struct {
	logger *logger.Logger

	doneChan             chan struct{}
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	requestId           string
	serviceAccountToken string
	kubeHost            string
	targetGroups        []string
	targetUser          string
}

func New(logger *logger.Logger,
	ch chan smsg.StreamMessage,
	doneChan chan struct{},
	serviceAccountToken string,
	kubeHost string,
	targetGroups []string,
	targetUser string) *StreamAction {
	return &StreamAction{
		logger:              logger,
		streamOutputChan:    ch,
		doneChan:            doneChan,
		serviceAccountToken: serviceAccountToken,
		kubeHost:            kubeHost,
		targetGroups:        targetGroups,
		targetUser:          targetUser,
	}
}

func (s *StreamAction) Kill() {
	close(s.doneChan)
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

		return s.startStream(streamActionRequest, action)
	case stream.StreamStop:
		var streamActionRequest stream.KubeStreamActionPayload
		if err := json.Unmarshal(actionPayload, &streamActionRequest); err != nil {
			rerr := fmt.Errorf("malformed Kube Stream Action payload %v", actionPayload)
			s.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		s.logger.Info("Stopping Stream Action")
		close(s.doneChan) // close the go routines

		return "", []byte{}, nil
	default:
		rerr := fmt.Errorf("unhandled stream action: %v", action)
		s.logger.Error(rerr)
		return "", []byte{}, rerr
	}
}

func (s *StreamAction) startStream(streamActionRequest stream.KubeStreamActionPayload, action string) (string, []byte, error) {
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
		defer cancel()
		s.logger.Error(err)
		return action, []byte{}, err
	}

	// Make the request and wait for the body to close
	req = req.WithContext(ctx)
	httpClient := &http.Client{}
	res, err := httpClient.Do(req)
	if err != nil {
		defer cancel()
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
	switch s.streamMessageVersion {
	// prior to 202204
	case "":
		s.sendStreamMessage(0, smsg.StreamData, true, kubeWatchHeadersPayloadBytes[:], streamActionRequest.LogId)
	default:
		s.sendStreamMessage(0, smsg.Data, true, kubeWatchHeadersPayloadBytes[:], streamActionRequest.LogId)
	}

	// Create our bufio object
	buf := make([]byte, 1024)
	br := bufio.NewReader(res.Body)

	sequenceNumber := 1

	go func() {
		defer res.Body.Close()
		for {
			// Read into the buffer
			numBytes, err := br.Read(buf)

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
				switch s.streamMessageVersion {
				// prior to 202204
				case "":
					s.sendStreamMessage(sequenceNumber, smsg.StreamEnd, false, buf[:numBytes], streamActionRequest.LogId)
				default:
					s.sendStreamMessage(sequenceNumber, smsg.Stream, false, buf[:numBytes], streamActionRequest.LogId)
				}
				return
			}

			// Stream the response back
			switch s.streamMessageVersion {
			// prior to 202204
			case "":
				s.sendStreamMessage(sequenceNumber, smsg.StreamData, true, buf[:numBytes], streamActionRequest.LogId)
			default:
				s.sendStreamMessage(sequenceNumber, smsg.Data, true, buf[:numBytes], streamActionRequest.LogId)
			}
			sequenceNumber += 1
		}
	}()

	// Subscribe to our done channel
	go func() {
		<-s.doneChan
		cancel()
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
			if bodyBytes, err := io.ReadAll(noFollowRes.Body); err == nil {
				// Stream the context back to the user
				switch s.streamMessageVersion {
				// prior to 202204
				case "":
					s.sendStreamMessage(sequenceNumber, smsg.StreamData, true, bodyBytes, streamActionRequest.LogId)
				default:
					s.sendStreamMessage(sequenceNumber, smsg.Data, true, bodyBytes, streamActionRequest.LogId)
				}
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
	more bool,
	contentBytes []byte,
	logId string,
) {
	s.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion:  s.streamMessageVersion,
		SequenceNumber: sequenceNumber,
		RequestId:      s.requestId,
		LogId:          logId,
		Action:         string(kubeaction.Stream),
		Type:           streamType,
		More:           more,
		Content:        base64.StdEncoding.EncodeToString(contentBytes),
	}
}
