package portforward

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	"k8s.io/apimachinery/pkg/util/httpstream"
)

// getStreamPair returns a httpStreamPair for requestID. This creates a
// new pair if one does not yet exist for the requestID. The returned bool is
// true if the pair was created.
func (p *PortForwardAction) getStreamPair(requestID string) (*httpStreamPair, bool) {
	p.streamPairsLock.Lock()
	defer p.streamPairsLock.Unlock()

	if portforwardStreamPair, ok := p.streamPairs[requestID]; ok {
		p.logger.Infof("Request %s, found existing stream pair", requestID)
		return portforwardStreamPair, false
	}

	p.logger.Infof("Request %s, creating new stream pair.", requestID)

	portforwardStreamPair := newPortForwardPair(requestID)
	p.streamPairs[requestID] = portforwardStreamPair

	return portforwardStreamPair, true
}

// newPortForwardPair creates a new httpStreamPair.
func newPortForwardPair(requestID string) *httpStreamPair {
	return &httpStreamPair{
		requestID: requestID,
		complete:  make(chan struct{}),
	}
}

// add adds the stream to the httpStreamPair. If the pair already
// contains a stream for the new stream's type, an error is returned. add
// returns true if both the data and error streams for this pair have been
// received.
func (p *httpStreamPair) add(stream httpstream.Stream) (bool, error) {
	p.lock.Lock()
	defer p.lock.Unlock()

	switch stream.Headers().Get(kubeutils.StreamType) {
	case kubeutils.StreamTypeError:
		if p.errorStream != nil {
			return false, errors.New("error stream already assigned")
		}
		p.errorStream = stream
	case kubeutils.StreamTypeData:
		if p.dataStream != nil {
			return false, errors.New("data stream already assigned")
		}
		p.dataStream = stream
	}

	complete := p.errorStream != nil && p.dataStream != nil
	if complete {
		close(p.complete)
	}
	return complete, nil
}

// printError writes s to p.errorStream if p.errorStream has been set.
func (p *httpStreamPair) printError(s string) {
	p.lock.RLock()
	defer p.lock.RUnlock()
	if p.errorStream != nil {
		fmt.Fprint(p.errorStream, s)
	}
}

// httpStreamReceived is the httpstream.NewStreamHandler for port
// forward streams. It checks each stream's port and stream type headers,
// rejecting any streams that with missing or invalid values. Each valid
// stream is sent to the streams channel.
func (p *PortForwardAction) httpStreamReceived(ctx context.Context, streams chan httpstream.Stream) func(httpstream.Stream, <-chan struct{}) error {
	return func(stream httpstream.Stream, replySent <-chan struct{}) error {
		// make sure it has a valid port header
		portString := stream.Headers().Get(kubeutils.PortHeader)
		if len(portString) == 0 {
			return fmt.Errorf("%q header is required", kubeutils.PortHeader)
		}
		port, err := strconv.ParseUint(portString, 10, 16)
		if err != nil {
			return fmt.Errorf("unable to parse %q as a port: %v", portString, err)
		}
		if port < 1 {
			return fmt.Errorf("port %q must be > 0", portString)
		}

		// make sure it has a valid stream type header
		streamType := stream.Headers().Get(kubeutils.StreamType)
		if len(streamType) == 0 {
			return fmt.Errorf("%q header is required", kubeutils.StreamType)
		}
		if streamType != kubeutils.StreamTypeError && streamType != kubeutils.StreamTypeData {
			return fmt.Errorf("invalid stream type %q", streamType)
		}

		select {
		case streams <- stream:
			return nil
		case <-ctx.Done():
			return errors.New("request has been cancelled")
		}
	}
}

// monitorStreamPair waits for the pair to receive both its error and data
// streams, or for the timeout to expire (whichever happens first), and then
// removes the pair.
func (p *PortForwardAction) monitorStreamPair(portforwardStreamPair *httpStreamPair, timeout <-chan time.Time) {
	select {
	case <-timeout:
		p.logger.Error(fmt.Errorf("request %s, timed out waiting for streams", portforwardStreamPair.requestID))
	case <-portforwardStreamPair.complete:
		p.logger.Infof("Request %s, successfully received error and data streams.", portforwardStreamPair.requestID)
	}
	p.removeStreamPair(portforwardStreamPair.requestID)
}

// removeStreamPair removes the stream pair identified by requestID from streamPairs.
func (p *PortForwardAction) removeStreamPair(requestID string) {
	p.streamPairsLock.Lock()
	defer p.streamPairsLock.Unlock()

	delete(p.streamPairs, requestID)
}
