package dial

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/db/actions/dial"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
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
		if err := json.Unmarshal(actionPayload, &dialActionRequest); err != nil {
			err = fmt.Errorf("malformed dial action payload %v", actionPayload)
			break
		}

		return d.startDial(dialActionRequest, action)
	case dial.DialInput:
		// Deserialize the action payload, the only action passed is DataIn
		var dataIn dial.DialInputActionPayload
		if err := json.Unmarshal(actionPayload, &dataIn); err != nil {
			err = fmt.Errorf("unable to unmarshal dial input message: %s", err)
			break
		}
		d.logger.Infof("PASSED REQ ID: %s", dataIn.RequestId)
		d.logger.Infof("SAVED REQ ID: %s", d.requestId)
		d.logger.Infof("ACTION: %s", action)

		// First validate the requestId
		if err = d.validateRequestId(dataIn.RequestId); err != nil {
			break
		}

		// Then send the data to our remote connection, decode the data first
		dataToWrite, err := base64.StdEncoding.DecodeString(dataIn.Data)
		if err != nil {
			break
		}

		d.logger.Infof("BYTES RECV SERVER: %d", len(dataToWrite))

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
		d.logger.Infof("PASSED REQ ID: %s", dataEnd.RequestId)
		d.logger.Infof("SAVED REQ ID: %s", d.requestId)
		d.logger.Infof("ACTION: %s", action)

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

func (d *Dial) startDial(dialActionRequest dial.DialActionPayload, action string) (string, []byte, error) {
	// Set our requestId
	d.requestId = dialActionRequest.RequestId

	// For each start, call the dial the TCP address
	remoteConnection, err := net.DialTCP("tcp", nil, d.remoteAddress)
	if err != nil {
		d.logger.Errorf("Failed to dial remote address: %s", err)
		// Let our daemon know that we have got the error and we need to close the connection
		// message := smsg.StreamMessage{
		// 	Type:           string(smsg.DbAgentClose),
		// 	RequestId:      d.requestId,
		// 	SequenceNumber: 0,
		// 	Content:        "", // No content for dbAgent Close
		// 	LogId:          "", // No log id for db messages
		// }
		// d.logger.Infof("HERE?: %s", d.streamOutputChan)
		// d.streamOutputChan <- message
		// d.logger.Infof("AFTER??: %s", d.streamOutputChan)

		// // Ensure that we close the dial action
		// d.closed = true

		// // Close this connection
		// // remoteConnection.Close()

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
					d.logger.Errorf("Read failed '%s'\n", err)
				}
				d.logger.Errorf("HERE RCON ERR: %s", err)

				// Let our daemon know that we have got the error and we need to close the connection
				message := smsg.StreamMessage{
					Type:           string(smsg.DbAgentClose),
					RequestId:      d.requestId,
					SequenceNumber: sequenceNumber,
					Content:        "", // No content for dbAgent Close
					LogId:          "", // No log id for db messages
				}
				d.streamOutputChan <- message

				// Ensure that we close the dial action
				d.closed = true

				// Close this connection
				remoteConnection.Close()
				return
			}

			tcpBytesBuffer := buff[:n]

			d.logger.Infof("Received %d bytes from local tcp connection, sending to bastion", n)

			// Now send this to bastion
			str := base64.StdEncoding.EncodeToString(tcpBytesBuffer)
			message := smsg.StreamMessage{
				Type:           string(smsg.DbOut),
				RequestId:      d.requestId,
				SequenceNumber: sequenceNumber,
				Content:        str,
				LogId:          "", // No log id for db messages
			}
			d.streamOutputChan <- message

			sequenceNumber += 1
		}
	}()

	// Update our remote connection
	d.remoteConnection = remoteConnection
	return action, []byte{}, nil
}

func (d *Dial) validateRequestId(requestId string) error {
	if requestId != d.requestId {
		rerr := fmt.Errorf("invalid request ID passed")
		d.logger.Error(rerr)
		return rerr
	}
	return nil
}
