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
	"gopkg.in/tomb.v2"
	"k8s.io/apimachinery/pkg/util/httpstream"
)

type PortForwardRequest struct {
	tmb    *tomb.Tomb
	logger *logger.Logger

	requestId            string
	portForwardRequestId string
	logId                string

	dataHeaders  map[string]string
	errorHeaders map[string]string

	// To send data/error to our portforward sessions
	portforwardDataInChannel  chan []byte
	portforwardErrorInChannel chan []byte

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	// Done channel so the go routines can communicate with eachother
	doneChan chan bool
}

func createPortForwardRequest(
	logger *logger.Logger,
	tmb *tomb.Tomb,
	streamOutputChan chan smsg.StreamMessage,
	version smsg.SchemaVersion,
	requestId string,
	logId string,
	portForwardRequestId string,
	podPort int64,
	dataHeaders map[string]string,
	errorHeaders map[string]string,
) *PortForwardRequest {
	p := &PortForwardRequest{
		logger:                    logger,
		streamOutputChan:          streamOutputChan,
		streamMessageVersion:      version,
		portforwardDataInChannel:  make(chan []byte),
		portforwardErrorInChannel: make(chan []byte),
		tmb:                       tmb,
		doneChan:                  make(chan bool),
		requestId:                 requestId,
		logId:                     logId,
		portForwardRequestId:      portForwardRequestId,
		dataHeaders:               dataHeaders,
		errorHeaders:              errorHeaders,
	}

	// Update our error headers to include the podPort
	p.errorHeaders[kubeutils.PortHeader] = fmt.Sprintf("%d", podPort)
	p.errorHeaders[kubeutils.PortForwardRequestIDHeader] = p.portForwardRequestId
	return p
}

func (p *PortForwardRequest) openPortForwardStream(streamConn httpstream.Connection) error {

	// Create our two streams with the provided headers
	// We purposely share the header object for data and error stream
	headers := http.Header{}
	for name, value := range p.errorHeaders {
		headers.Add(name, value)
	}
	// Create our http.Header
	errorStream, err := streamConn.CreateStream(headers)
	if err != nil {
		rerr := fmt.Errorf("error creating error stream: %s", err)
		p.logger.Error(rerr)
		return rerr
	}

	for name, value := range p.dataHeaders {
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
			case <-p.tmb.Dying():
				return
			case <-p.doneChan:
				errorStream.Close()
				dataStream.Close()
				return
			case dataInMessage := <-p.portforwardDataInChannel:
				// Make this request locally, and then return that info to the user
				if _, err := io.Copy(dataStream, bytes.NewReader(dataInMessage)); err != nil {
					p.logger.Error(fmt.Errorf("error writing to data stream: %s", err))
					p.doneChan <- true
					dataStream.Close()
					return
				}
			case errorInMessage := <-p.portforwardErrorInChannel:
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
			case <-p.tmb.Dying():
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
			case <-p.tmb.Dying():
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
