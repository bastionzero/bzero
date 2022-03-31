package dial

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	dbaction "bastionzero.com/bctl/v1/bzerolib/plugin/db"
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
	remoteHost string,
	remotePort int) (*Dial, error) {

	// Build our address
	address := fmt.Sprintf("%s:%v", remoteHost, remotePort)

	// Open up a connection to the TCP addr we are trying to connect to
	if raddr, err := net.ResolveTCPAddr("tcp", address); err != nil {
		logger.Errorf("Failed to resolve remote address: %s", err)
		return nil, fmt.Errorf("failed to resolve remote address: %s", err)
	} else {
		return &Dial{
			logger:           logger,
			tmb:              pluginTmb,
			closed:           false,
			streamOutputChan: ch,
			remoteAddress:    raddr,
		}, nil
	}
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

		// Deserialize the action payload
		var dataEnd dial.DialActionPayload
		if jerr := json.Unmarshal(actionPayload, &dataEnd); jerr != nil {
			err = fmt.Errorf("unable to unmarshal dial input message: %s", jerr)
			break
		}

		d.closed = true // Ensure that we close the dial action
		d.remoteConnection.Close()

		// give our streamoutputchan time to process all the messages we sent while the stop request was getting here
		return action, actionPayload, nil
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
	if remoteConnection, err := net.DialTCP("tcp", nil, d.remoteAddress); err != nil {
		d.logger.Errorf("Failed to dial remote address: %s", err)
		return action, []byte{}, err
	} else {
		d.remoteConnection = remoteConnection
	}

	// set up a go routine to listen to the tomb dying so that we can interrupt our listening routine
	go func() {
		<-d.tmb.Dying()
		d.remoteConnection.Close()
	}()

	// Setup a go routine to listen for messages coming from this local connection and send to daemon
	go func() {
		defer d.remoteConnection.Close()
		defer func() {
			d.closed = true
		}()

		sequenceNumber := 0
		buff := make([]byte, chunkSize)

		for {
			// this line blocks until it reads output or error
			n, err := d.remoteConnection.Read(buff)

			if d.closed {
				return
			}

			if err != nil {
				if err != io.EOF {
					d.logger.Errorf("failed to read from tcp connection: %s", err)
				} else {
					d.logger.Errorf("connection closed")
				}

				// Let our daemon know that we have got the error and we need to close the connection
				content := base64.StdEncoding.EncodeToString(buff[:n])
				d.sendStreamMessage(sequenceNumber, smsg.Stream, false, content)

				return
			}

			d.logger.Debugf("Sending %d bytes from local tcp connection to daemon", n)

			// Now send this to daemon
			content := base64.StdEncoding.EncodeToString(buff[:n])
			d.sendStreamMessage(sequenceNumber, smsg.Stream, true, content)

			sequenceNumber += 1
		}
	}()

	// Update our remote connection
	return action, []byte{}, nil
}

func (d *Dial) sendStreamMessage(sequenceNumber int, streamType smsg.StreamType, more bool, content string) {
	message := smsg.StreamMessage{
		SchemaVersion:  smsg.CurrentSchema,
		SequenceNumber: sequenceNumber,
		Action:         string(dbaction.Dial),
		Type:           streamType,
		More:           more,
		Content:        content,
	}
	d.streamOutputChan <- message
}
