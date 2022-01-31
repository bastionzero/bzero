package utils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/httpstream/spdy"

	"bastionzero.com/bctl/v1/bzerolib/logger"
)

// Some of the code here is taken from teleports open source repo (Apache 2.0)
// which can be found here:
// * https://github.com/gravitational/teleport/blob/9b8b9d6d0c115d43d31d53c47db3050e27edbc4a/lib/kube/proxy/remotecommand.go

type ExecSPDYService struct {
	StdinStream  io.ReadCloser
	StdoutStream io.WriteCloser
	StderrStream io.WriteCloser
	WriteStatus  func(status *StatusError) error
	ResizeStream io.ReadCloser
	Service      SPDYService
}

type SPDYService struct {
	Conn   io.Closer
	logger *logger.Logger
}

type streamAndReply struct {
	httpstream.Stream
	replySent <-chan struct{}
}

type StatusError struct {
	ErrStatus metav1.Status
}

func NewExecSPDYService(logger *logger.Logger, writer http.ResponseWriter, request *http.Request, expectedStreams int) (*ExecSPDYService, error) {
	// Build a generic spdy serice
	service, streamCh, err := NewSPDYService(logger, writer, request, expectedStreams)
	if err != nil {
		return nil, fmt.Errorf("could nor create service: %v", err.Error())
	}

	// Build our object
	spdyService := &ExecSPDYService{
		Service: *service,
	}

	// Wait for our streams to come in
	expired := time.NewTimer(DefaultStreamCreationTimeout)
	defer expired.Stop()

	// Wait for streams to come in and return SPDY service
	if err := spdyService.waitForStreamsExec(request.Context(), streamCh, expectedStreams, expired.C); err != nil {
		return nil, fmt.Errorf("error waiting for streams to come in: %v", err.Error())
	} else {
		return spdyService, nil
	}
}

func NewSPDYService(logger *logger.Logger, writer http.ResponseWriter, request *http.Request, expectedStreams int) (*SPDYService, chan streamAndReply, error) {
	// Initiate a handshake and upgrade the request
	supportedProtocols := []string{"v4.channel.k8s.io", "v3.channel.k8s.io", "v2.channel.k8s.io", "channel.k8s.io"}
	protocol, err := httpstream.Handshake(request, writer, supportedProtocols)
	if err != nil {
		return nil, nil, fmt.Errorf("could not complete http stream handshake: %v", err.Error())
	}
	logger.Infof("Using protocol: %s\n", protocol)

	// Now make our stream channel
	streamCh := make(chan streamAndReply)
	upgrader := spdy.NewResponseUpgrader()
	conn := upgrader.UpgradeResponse(writer, request, func(stream httpstream.Stream, replySent <-chan struct{}) error {
		streamCh <- streamAndReply{Stream: stream, replySent: replySent}
		return nil
	})
	if conn == nil {
		// The upgrader is responsible for notifying the client of any errors that
		// occurred during upgrading. All we can do is return here at this point
		// if we weren't successful in upgrading.
		// TODO: Return a better error
		logger.Error(fmt.Errorf("unable to upgrade request"))
		return nil, nil, fmt.Errorf("unable to upgrade request")
	}

	// Set our idle timeout, set to 4 hours as that is what the kubelet uses by default
	// Ref: https://github.com/kubernetes/kubernetes/issues/66661#issuecomment-411324031
	conn.SetIdleTimeout(DefaultIdleTimeout)

	service := &SPDYService{
		Conn:   conn,
		logger: logger,
	}

	return service, streamCh, nil
}

func (s *ExecSPDYService) waitForStreamsExec(connContext context.Context,
	streams <-chan streamAndReply,
	expectedStreams int,
	expired <-chan time.Time) error {

	// Ref: https://github.com/gravitational/teleport/blob/7bff7c41bda0f36898e9063aaacd5539ce062489/lib/kube/proxy/remotecommand.go
	// Make our command object
	receivedStreams := 0
	replyChan := make(chan struct{})
	stopCtx, cancel := context.WithCancel(connContext)
	defer cancel()

WaitForStreams:
	for {
		select {
		// Loop over all incoming strems until we see all expected steams
		case stream := <-streams:
			// Extract the stream type from the header
			streamType := stream.Headers().Get(StreamType)
			s.Service.logger.Infof("Saw stream type: " + streamType)

			// Save the stream
			switch streamType {
			case StreamTypeError:
				s.WriteStatus = v4WriteStatusFunc(stream)
				// Send to a buffer to wait, we will wait on replyChan until we see the expected number of streams
				go waitStreamReply(stopCtx, stream.replySent, replyChan)
			case StreamTypeStdin:
				s.StdinStream = stream
				go waitStreamReply(stopCtx, stream.replySent, replyChan)
			case StreamTypeStdout:
				s.StdoutStream = stream
				go waitStreamReply(stopCtx, stream.replySent, replyChan)
			case StreamTypeStderr:
				s.StderrStream = stream
				go waitStreamReply(stopCtx, stream.replySent, replyChan)
			case StreamTypeResize:
				s.ResizeStream = stream
				go waitStreamReply(stopCtx, stream.replySent, replyChan)
			default:
				fmt.Printf("Ignoring unexpected stream type: %q", streamType)
			}
		case <-replyChan:
			receivedStreams++
			if receivedStreams == expectedStreams {
				break WaitForStreams
			}
		case <-expired:
			return errors.New("timed out waiting for client to create streams")
		case <-connContext.Done():
			return errors.New("connection has dropped, exiting")
		}
	}
	return nil
}

// v4WriteStatusFunc returns a WriteStatusFunc that marshals a given api Status
// as json in the error channel.
func v4WriteStatusFunc(stream io.Writer) func(status *StatusError) error {
	return func(status *StatusError) error {
		bs, err := json.Marshal(status.ErrStatus)
		if err != nil {
			return err
		}
		_, err = stream.Write(bs)
		return err
	}
}

// waitStreamReply waits until either replySent or stop is closed. If replySent is closed, it sends
// an empty struct to the notify channel.
func waitStreamReply(ctx context.Context, replySent <-chan struct{}, notify chan<- struct{}) {
	select {
	case <-replySent:
		notify <- struct{}{}
	case <-ctx.Done():
	}
}
