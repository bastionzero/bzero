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
	"bastionzero.com/bctl/v1/bzerolib/plugin/db"
	"bastionzero.com/bctl/v1/bzerolib/plugin/db/actions/dial"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

const (
	chunkSize      = 64 * 1024
	writeDeadline  = 5 * time.Second
	dialTCPTimeout = 30 * time.Second
)

type Dial struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	// channel for letting the plugin know we're done
	doneChan chan struct{}

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	requestId        string
	remoteAddress    *net.TCPAddr
	remoteConnection *net.Conn
}

func New(logger *logger.Logger,
	ch chan smsg.StreamMessage,
	doneChan chan struct{},
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
			doneChan:         doneChan,
			streamOutputChan: ch,
			remoteAddress:    raddr,
		}, nil
	}
}

func (d *Dial) Kill() {
	if !d.tmb.Alive() {
		return
	}

	d.tmb.Kill(nil)
	if d.remoteConnection != nil {
		(*d.remoteConnection).Close()
	}
	d.tmb.Wait()
}

func (d *Dial) Receive(action string, actionPayload []byte) ([]byte, error) {
	var err error

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

			// Set a deadline for the write so we don't block forever
			(*d.remoteConnection).SetWriteDeadline(time.Now().Add(writeDeadline))
			if _, err := (*d.remoteConnection).Write(dataToWrite); !d.tmb.Alive() {
				return []byte{}, nil
			} else if err != nil {
				d.logger.Errorf("error writing to local TCP connection: %s", err)
				d.Kill()
			}
		}

	case dial.DialStop:
		d.Kill()
		return actionPayload, nil
	default:
		err = fmt.Errorf("unhandled stream action: %v", action)
	}

	if err != nil {
		d.logger.Error(err)
	}
	return []byte{}, err
}

func (d *Dial) start(dialActionRequest dial.DialActionPayload, action string) ([]byte, error) {
	// keep track of who we're talking to
	d.requestId = dialActionRequest.RequestId
	d.logger.Infof("Setting request id: %s", d.requestId)
	d.streamMessageVersion = dialActionRequest.StreamMessageVersion
	d.logger.Infof("Setting stream message version: %s", d.streamMessageVersion)

	// For each start, call the dial the TCP address
	if remoteConnection, err := net.DialTimeout("tcp", d.remoteAddress.String(), dialTCPTimeout); err != nil {
		d.logger.Errorf("Failed to dial remote address: %s", err)
		return []byte{}, err
	} else {
		d.remoteConnection = &remoteConnection
	}

	// Setup a go routine to listen for messages coming from this local connection and send to daemon
	d.tmb.Go(func() error {
		defer close(d.doneChan)

		sequenceNumber := 0
		buff := make([]byte, chunkSize)

		for {
			// this line blocks until it reads output or error
			if n, err := (*d.remoteConnection).Read(buff); !d.tmb.Alive() {
				return nil
			} else if err != nil {
				if err == io.EOF {
					d.logger.Errorf("connection closed")

					// Let our daemon know that we have got the error and we need to close the connection
					switch d.streamMessageVersion {
					// prior to 202204
					case "":
						d.sendStreamMessage(sequenceNumber, smsg.DbStreamEnd, false, buff[:n])
					default:
						d.sendStreamMessage(sequenceNumber, smsg.Stream, false, buff[:n])
					}
				} else {
					d.logger.Errorf("failed to read from tcp connection: %s", err)
					d.sendStreamMessage(sequenceNumber, smsg.Error, false, []byte(err.Error()))
				}

				return err
			} else {
				d.logger.Debugf("Sending %d bytes from local tcp connection to daemon", n)

				// Now send this to daemon
				switch d.streamMessageVersion {
				// prior to 202204
				case "":
					d.sendStreamMessage(sequenceNumber, smsg.DbStream, true, buff[:n])
				default:
					d.sendStreamMessage(sequenceNumber, smsg.Stream, true, buff[:n])
				}

				sequenceNumber += 1
			}
		}
	})

	// Update our remote connection
	return []byte{}, nil
}

func (d *Dial) sendStreamMessage(sequenceNumber int, streamType smsg.StreamType, more bool, contentBytes []byte) {
	d.streamOutputChan <- smsg.StreamMessage{
		SchemaVersion:  d.streamMessageVersion,
		SequenceNumber: sequenceNumber,
		Action:         string(db.Dial),
		Type:           streamType,
		More:           more,
		Content:        base64.StdEncoding.EncodeToString(contentBytes),
	}
}
