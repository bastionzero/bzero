package portforward

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	kubeaction "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/portforward"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"k8s.io/apimachinery/pkg/util/httpstream"
)

type PortForwardRequest struct {
	logger               *logger.Logger
	streamOutputChan     chan smsg.StreamMessage // output channel to send all of our stream messages directly to datachannel
	streamMessageVersion smsg.SchemaVersion

	requestId            string
	logId                string
	portForwardRequestId string

	// To send data/error to our portforward sessions
	dataInChannel  chan []byte
	errorInChannel chan []byte
	doneChan       chan bool // Done channel so the go routines can communicate with eachother
	parentDoneChan chan struct{}
}

func createPortForwardRequest(
	logger *logger.Logger,
	doneChan chan struct{},
	streamOutputChan chan smsg.StreamMessage,
	version smsg.SchemaVersion,
	requestId string,
	logId string,
	portForwardRequestId string,
) *PortForwardRequest {
	return &PortForwardRequest{
		logger:               logger,
		streamOutputChan:     streamOutputChan,
		streamMessageVersion: version,

		requestId:            requestId,
		logId:                logId,
		portForwardRequestId: portForwardRequestId,

		dataInChannel:  make(chan []byte),
		errorInChannel: make(chan []byte),
		doneChan:       make(chan bool),
		parentDoneChan: doneChan,
	}
}

func (p *PortForwardRequest) openPortForwardStream(dataHeaders map[string]string, errorHeaders map[string]string, podPort int64, streamConn httpstream.Connection) error {

	// Create our two streams with the provided headers
	// We purposely share the header object for data and error stream
	headers := http.Header{}
	for name, value := range errorHeaders {
		headers.Add(name, value)
	}
	headers.Add(kubeutils.PortHeader, fmt.Sprintf("%d", podPort))
	headers.Add(kubeutils.PortForwardRequestIDHeader, p.portForwardRequestId)

	// Create our http.Header
	errorStream, err := streamConn.CreateStream(headers)
	if err != nil {
		rerr := fmt.Errorf("error creating error stream: %s", err)
		p.logger.Error(rerr)
		return rerr
	}

	for name, value := range dataHeaders {
		// Set so we override any error headers that were set
		headers.Set(name, value)
	}
	// Create our http.Header
	dataStream, err := streamConn.CreateStream(headers)
	if err != nil {
		rerr := fmt.Errorf("error creating data stream: %s", err)
		p.logger.Error(rerr)
		return rerr
	}

	// We need to set up two listeners for our data/error-in channel (i.e. coming from the user)
	go func() {
		for {
			select {
			case <-p.parentDoneChan:
				return
			case <-p.doneChan:
				errorStream.Close()
				dataStream.Close()
				return
			case dataInMessage := <-p.dataInChannel:
				// Make this request locally, and then return that info to the user
				if _, err := io.Copy(dataStream, bytes.NewReader(dataInMessage)); err != nil {
					p.logger.Error(fmt.Errorf("error writing to data stream: %s", err))
					p.doneChan <- true
					dataStream.Close()
					return
				}
			case errorInMessage := <-p.errorInChannel:
				// Make this request locally, and then return that info to the user
				if _, err := io.Copy(errorStream, bytes.NewReader(errorInMessage)); err != nil {
					p.logger.Error(fmt.Errorf("error writing to error stream: %s", err))

					// Do not alert on anything
					return
				}
			}
		}
	}()

	// Set up a go routine to listen for to our dataStream and send to the client
	go func() {
		defer dataStream.Close()

		// Keep track of seq number
		dataSeqNumber := 0

		for {
			select {
			case <-p.parentDoneChan:
				return
			default:
				p.forwardStream(smsg.Data, dataStream, dataSeqNumber)
				dataSeqNumber += 1
			}
		}
	}()

	// Setup a go routine for the error stream as well
	go func() {
		defer errorStream.Close()

		// Keep track of seq number
		errorSeqNumber := 0

		for {
			select {
			case <-p.parentDoneChan:
				return
			default:
				p.forwardStream(smsg.Error, errorStream, errorSeqNumber)
				errorSeqNumber += 1
			}
		}
	}()

	return nil
}

// NOTE: we don't need to check version here because Portforward is broken on previous versions of bzero
// thus, anyone using it at all is using the new version
func (p *PortForwardRequest) forwardStream(streamType smsg.StreamType, stream httpstream.Stream, sequenceNumber int) {
	buf := make([]byte, portforward.DataStreamBufferSize)
	n, err := stream.Read(buf)
	if err != nil {
		if err != io.EOF {
			rerr := fmt.Errorf("error reading data from data stream: %s", err)
			p.logger.Error(rerr)
		} else if streamType == smsg.Data {
			content, err := p.wrapStreamMessageContent([]byte{})
			if err != nil {
				p.logger.Error(err)

				// Alert on our done channel
				p.doneChan <- true
			}

			// NOTE: we don't have to version this because this part of portforward is broken prior to 202204
			p.sendStreamMessage(sequenceNumber, streamType, false, content)
		}
		p.doneChan <- true
		return
	}

	// Send this data back to the bastion
	content, err := p.wrapStreamMessageContent(buf[:n])
	if err != nil {
		p.logger.Error(err)

		// Alert on our done channel
		p.doneChan <- true
	}
	// NOTE: we don't have to version this because this part of portforward is broken prior to 202204
	p.sendStreamMessage(sequenceNumber, streamType, true, content)
}

func (p *PortForwardRequest) wrapStreamMessageContent(content []byte) (string, error) {
	streamMessageToSend := portforward.KubePortForwardStreamMessageContent{
		PortForwardRequestId: p.portForwardRequestId,
		Content:              content,
	}
	streamMessageToSendBytes, err := json.Marshal(streamMessageToSend)
	if err != nil {
		rerr := fmt.Errorf("error marsheling stream message: %s", err)

		return "", rerr
	}

	return base64.StdEncoding.EncodeToString(streamMessageToSendBytes), nil
}

func (p *PortForwardRequest) sendStreamMessage(sequenceNumber int, streamType smsg.StreamType, more bool, content string) {
	p.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion:  p.streamMessageVersion,
		SequenceNumber: sequenceNumber,
		RequestId:      p.requestId,
		LogId:          p.logId,
		Action:         string(kubeaction.PortForward),
		Type:           streamType,
		More:           more,
		Content:        content,
	}
}
