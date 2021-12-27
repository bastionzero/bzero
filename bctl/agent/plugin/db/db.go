package db

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	"gopkg.in/tomb.v2"
)

type DbAction string

const (
	Init   DbAction = "init"
	Start  DbAction = "start"
	DataIn DbAction = "datain"
)

type DbActionParams struct {
}

type IDbAction interface {
	Receive(action string, actionPayload []byte) (string, []byte, error)
	Closed() bool
}

type DbPlugin struct {
	tmb *tomb.Tomb // datachannel's tomb

	logger *logger.Logger

	streamOutputChan chan smsg.StreamMessage

	targetPort string
	targetHost string

	remoteAddress *net.TCPAddr

	// Keep track of all the dials TCP connections
	actions map[string]IDbAction
}

func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	ch chan smsg.StreamMessage,
	payload []byte) (*DbPlugin, error) {

	// Unmarshal the Syn payload
	var synPayload DbActionParams
	if err := json.Unmarshal(payload, &synPayload); err != nil {
		return &DbPlugin{}, fmt.Errorf("malformed Db plugin SYN payload %v", string(payload))
	}

	// Open up a connection to the TCP addr we are trying to connect to
	raddr, err := net.ResolveTCPAddr("tcp", ":5432")
	if err != nil {
		logger.Errorf("Failed to resolve remote address: %s", err)
		return &DbPlugin{}, fmt.Errorf("failed to resolve remote address: %s", err)
	}

	plugin := &DbPlugin{
		targetPort:       "5432",
		targetHost:       "localhost",
		logger:           logger,
		tmb:              parentTmb, // if datachannel dies, so should we
		streamOutputChan: ch,
		remoteAddress:    raddr,
		actions:          make(map[string]IDbAction),
	}

	// Setup a go routine to listen for messages coming from this local connection and forward to the client
	// TODO: Setup tomb for this to be cancelled
	// This needs to be its own action
	go func() {
		// For each start, call the dial function
		rconn, err := net.DialTCP("tcp", nil, raddr)
		if err != nil {
			logger.Errorf("Failed to dial remote address: %s", err)
			// Let the agent know that there was an error
			return
		}

		buff := make([]byte, 0xffff)
		for {
			n, err := rconn.Read(buff)
			if err == io.EOF {
				// Let our daemon know that we have got the EOF error and we need to close the connection
				return
			}
			if err != nil {
				logger.Errorf("Read failed '%s'\n", err)

				// Let the daemon know that we received an error
				return
			}
			tcpBytesBuffer := buff[:n]

			logger.Infof("Received %d bytes from local tcp connection, sending to bastion", n)

			// Now send this to bastion
			str := base64.StdEncoding.EncodeToString(tcpBytesBuffer)
			message := smsg.StreamMessage{
				Type:           string(smsg.DbOut),
				RequestId:      "", // No requestId for db messages
				SequenceNumber: 0,  // No sequence number needed for db messages
				Content:        str,
				LogId:          "", // No log id for db messages
			}
			ch <- message
		}
	}()

	return plugin, nil
}

func (k *DbPlugin) Receive(action string, actionPayload []byte) (string, []byte, error) {
	// TODO: The below line removes the extra, surrounding quotation marks that get added at some point in the marshal/unmarshal
	// so it messes up the umarshalling into a valid action payload.  We need to figure out why this is happening
	// so that we can murder its family
	if len(actionPayload) > 0 {
		actionPayload = actionPayload[1 : len(actionPayload)-1]
	}

	// Json unmarshalling encodes bytes in base64
	actionPayloadSafe, base64Err := base64.StdEncoding.DecodeString(string(actionPayload))
	if base64Err != nil {
		k.logger.Errorf("error decoding actionPayload: %v", base64Err)
		return "", []byte{}, base64Err
	}

	// TODO: Create an action that starts a dial session
	// This needs to then update our action map, that way we can continue to send data to it in the background

	switch DbAction(action) {
	case DataIn:
		// Deserialize the action payload
		var dataIn DbDataInActionPayload
		if err := json.Unmarshal(actionPayloadSafe, &dataIn); err != nil {
			rerr := fmt.Errorf("unable to unmarshal dataIn message: %s", err)
			k.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		// Decode the data first
		dataToWrite, _ := base64.StdEncoding.DecodeString(dataIn.Data)

		// Find our which request we need to forward this data too by looking up the requestId in the actionMap

		// Send this data to our remote connection
		k.logger.Info("Received data from bastion, forwarding to remote tcp connection")
		// _, err := k.remoteConnection.Write(dataToWrite) // TODO: this returns an error, catch that error as well
		// if err != nil {
		// 	k.logger.Errorf("error writing to to remote connection: %v", err)
		// }

		return action, []byte{}, nil
	default:
		rerr := fmt.Errorf("unhandled db action: %v", action)
		k.logger.Error(rerr)
		return "", []byte{}, rerr
	}

}
