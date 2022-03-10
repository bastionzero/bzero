package dial

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/db/actions/dial"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize = 64 * 1024
)

type Dial struct {
	logger *logger.Logger
	tmb    *tomb.Tomb
	closed bool

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan chan smsg.StreamMessage

	requestId     string
	remoteAddress *net.TCPAddr

	remoteConnection *net.TCPConn
}

func New(logger *logger.Logger,
	pluginTmb *tomb.Tomb,
	ch chan smsg.StreamMessage,
	raddr *net.TCPAddr) (*Dial, error) {

	return &Dial{
		logger:           logger,
		tmb:              pluginTmb,
		closed:           false,
		streamOutputChan: ch,
		remoteAddress:    raddr,
	}, nil
}

func (d *Dial) Closed() bool {
	return d.closed
}

func (d *Dial) Receive(action string, actionPayload []byte) (string, []byte, error) {
	var err error

	// Update the logger action
	d.logger = d.logger.GetActionLogger(action)
	switch dial.DialSubAction(action) {
	case dial.DialStart:
		var dialActionRequest dial.DialActionPayload
		if err = json.Unmarshal(actionPayload, &dialActionRequest); err != nil {
			err = fmt.Errorf("malformed dial action payload %v", actionPayload)
			break
		}

		return d.start(dialActionRequest, action)
	case dial.DialInput:

		// Deserialize the action payload, the only action passed is input
		var dbInput dial.DialInputActionPayload
		if err = json.Unmarshal(actionPayload, &dbInput); err != nil {
			err = fmt.Errorf("unable to unmarshal dial input message: %s", err)
			break
		}

		// First validate the requestId
		if err = d.validateRequestId(dbInput.RequestId); err != nil {
			break
		}

		// Then send the data to our remote connection, decode the data first
		if dataToWrite, nerr := base64.StdEncoding.DecodeString(dbInput.Data); nerr != nil {
			err = nerr
			break
		} else {

			// Send this data to our remote connection
			d.logger.Info("Received data from daemon, forwarding to remote tcp connection")
			_, err = d.remoteConnection.Write(dataToWrite)
		}

	case dial.DialStop:
		d.remoteConnection.Close()
		d.closed = true

		// give our streamoutputchan time to process all the messages we sent while the stop request was getting here
		time.Sleep(2 * time.Second)

		// Send this data to our remote connection
		d.logger.Info("Received data from bastion, forwarding to remote tcp connection")
		_, err = d.remoteConnection.Write(dataToWrite)

	case dial.DialEnd:
		// Deserialize the action payload, the only action passed is DataIn
		var dataEnd dial.DialInputActionPayload
		if err := json.Unmarshal(actionPayload, &dataEnd); err != nil {
			err = fmt.Errorf("unable to unmarshal dial input message: %s", err)
			break
		}

		// First validate the requestId
		if err = d.validateRequestId(dataEnd.RequestId); err != nil {
			break
		}

		// Close the remote connection
		err = d.remoteConnection.Close()

		// Ensure that we close the dial action
		d.closed = true
	default:
		err = fmt.Errorf("unhandled stream action: %v", action)
	}

	if err != nil {
		d.logger.Error(err)
	}
	return "", []byte{}, err
}

func (d *Dial) start(dialActionRequest dial.DialActionPayload, action string) (string, []byte, error) {
	// Set our requestId
	d.requestId = dialActionRequest.RequestId
	d.logger.Infof("Setting request id: %s", d.requestId)

	// For each start, call the dial the TCP address
	remoteConnection, err := net.DialTCP("tcp", nil, d.remoteAddress)
	if err != nil {
		d.logger.Errorf("Failed to dial remote address: %s", err)
		return action, []byte{}, err
	}

	d.remoteConnection = remoteConnection

	// set up a go routine to listen to the tomb dying so that we can interrupt our listening routine
	go func() {
		<-d.tmb.Dying()
		remoteConnection.Close()
	}()

	// Setup a go routine to listen for messages coming from this local connection and send to daemon
	go func() {
		sequenceNumber := 1
		buff := make([]byte, chunkSize)

		for {
			// this line blocks until it reads output or error
			n, err := remoteConnection.Read(buff)

			if err != nil {
				if err != io.EOF {
					d.logger.Errorf("failed to read from tcp connection: %s", err)
				}

				// Let our daemon know that we have got the error and we need to close the connection
				d.sendStreamMessage(smsg.DbStreamEnd, sequenceNumber, "")

				// Ensure that we close the dial action
				d.closed = true

				// Close this connection
				remoteConnection.Close()
				return
			}

			d.logger.Infof("Sending %d bytes from local tcp connection to daemon", n)

			// Now send this to bastion
			content := base64.StdEncoding.EncodeToString(buff[:n])
			d.sendStreamMessage(smsg.DbStream, sequenceNumber, content)

			sequenceNumber += 1
		}
	}()

	// Update our remote connection
	return action, []byte{}, nil
}

func (d *Dial) sendStreamMessage(streamType smsg.StreamType, sequenceNumber int, content string) {
	message := smsg.StreamMessage{
		Type:           string(streamType),
		RequestId:      d.requestId,
		SequenceNumber: sequenceNumber,
		Content:        content,
		LogId:          "", // No log id for db messages
	}
	d.streamOutputChan <- message
}

func (d *Dial) validateRequestId(requestId string) error {
	if requestId != d.requestId {
		rerr := fmt.Errorf("invalid request ID passed")
		d.logger.Error(rerr)
		return rerr
	}
	return nil
}
