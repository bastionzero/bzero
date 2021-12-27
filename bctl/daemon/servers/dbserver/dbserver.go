package dbserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"

	agms "bastionzero.com/bctl/v1/bctl/agent/plugin/db"
	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"github.com/google/uuid"
	"gopkg.in/tomb.v2"
)

const (
	// websocket connection parameters for all datachannels created by http server
	autoReconnect = true
	getChallenge  = false
)

type DbServer struct {
	logger    *logger.Logger
	websocket *websocket.Websocket // TODO: This will need to be a dictionary for when we have multiple
	tmb       tomb.Tomb

	// Db connections only require a single datachannel
	datachannel *datachannel.DataChannel

	// Handler to select message types
	targetSelectHandler func(msg am.AgentMessage) (string, error)

	// fields for new datachannels
	daemonport          string
	params              map[string]string
	headers             map[string]string
	serviceUrl          string
	refreshTokenCommand string
	configPath          string
}

func StartDbServer(logger *logger.Logger,
	daemonPort string,
	refreshTokenCommand string,
	configPath string,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	targetSelectHandler func(msg am.AgentMessage) (string, error)) error {

	listener := &DbServer{
		logger:              logger,
		serviceUrl:          serviceUrl,
		params:              params,
		headers:             headers,
		targetSelectHandler: targetSelectHandler,
		configPath:          configPath,
		refreshTokenCommand: refreshTokenCommand,
		daemonport:          daemonPort,
	}

	// Create a new websocket
	if err := listener.newWebsocket(uuid.New().String()); err != nil {
		listener.logger.Error(err)
		return err
	}

	// Create a single datachannel for all of our db calls
	// if datachannel, err := listener.newDataChannel(string(db.Start), listener.websocket); err == nil {
	// 	listener.datachannel = datachannel
	// } else {
	// 	return err
	// }

	// Now create our local listener for TCP connections
	logger.Infof("Resolving TCP address for port: %s", daemonPort)
	localTcpAddress, err := net.ResolveTCPAddr("tcp", ":"+daemonPort)
	if err != nil {
		logger.Errorf("Failed to resolve TCP address %s", err)
		os.Exit(1)
	}

	logger.Infof("Setting up TCP lister")
	localTcpListener, err := net.ListenTCP("tcp", localTcpAddress)
	if err != nil {
		logger.Errorf("Failed to open local port to listen: %s", err)
		os.Exit(1)
	}

	// Block and keep listening for new tcp events
	for {
		conn, err := localTcpListener.AcceptTCP()
		if err != nil {
			logger.Errorf("Failed to accept connection '%s'", err)
			continue
		}

		go listener.handleProxy(conn, logger)
	}
}

func (h *DbServer) handleProxy(lconn *net.TCPConn, logger *logger.Logger) {
	// Always ensure we close the local tcp connection
	defer lconn.Close()

	// // Setup a go routine to listen for messages as well, and write to our local connection
	// // Now listen for messages from bastion
	// go func() {
	// 	for {
	// 		// Read incoming message(s)
	// 		_, rawMessage, err := client.ReadMessage()

	// 		if err != nil {
	// 			logger.Error(err)
	// 			return
	// 		} else {
	// 			// Unwrap the message
	// 			if messages, err := unwrapSignalR(rawMessage, logger); err != nil {
	// 				logger.Error(err)
	// 			} else {
	// 				for _, message := range messages {
	// 					logger.Infof("HERE: %s", message.Target)

	// 					//write out result
	// 					n, err := lconn.Write(message.Arguments[0].MessagePayload)
	// 					if err != nil {
	// 						logger.Errorf("Write failed '%s'\n", err)
	// 						return
	// 					}

	// 					logger.Infof("Wrote %d bytes to local tcp connection", n)
	// 				}
	// 			}
	// 		}
	// 	}
	// }()

	// Keep looping till we hit EOF
	tmp := make([]byte, 0xffff)
	for {
		n, err := lconn.Read(tmp)
		if err != nil {
			logger.Errorf("Read failed '%s'\n", err)
			return
		}

		buff := tmp[:n]
		h.logger.Infof("HERE: %s", buff)

		// signalRMessage := SignalRWrapper{
		// 	Target: "RequestDaemonToBastionV1",
		// 	Type:   signalRTypeNumber,
		// 	Arguments: []AgentMessage{{
		// 		ChannelId:      "test",
		// 		MessageType:    "keysplitting",
		// 		SchemaVersion:  "v1",
		// 		MessagePayload: buff,
		// 	}},
		// }

		// Write our message to websocket
		// if msgBytes, err := json.Marshal(signalRMessage); err != nil {
		// 	logger.Error(fmt.Errorf("error marshalling outgoing SignalR Message: %v", signalRMessage))
		// } else {
		// 	if err := client.WriteMessage(websocket.TextMessage, append(msgBytes, signalRMessageTerminatorByte)); err != nil {
		// 		logger.Error(err)
		// 	}
		// 	logger.Info("Send message to Bastion")
		// }
	}
}

// for creating new websockets
func (h *DbServer) newWebsocket(wsId string) error {
	subLogger := h.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, wsId, h.serviceUrl, h.params, h.headers, h.targetSelectHandler, autoReconnect, getChallenge, h.refreshTokenCommand, websocket.Db); err != nil {
		return err
	} else {
		h.websocket = wsClient
		return nil
	}
}

// for creating new datachannels
func (h *DbServer) newDataChannel(action string, websocket *websocket.Websocket) (*datachannel.DataChannel, error) {
	// every datachannel gets a uuid to distinguish it so a single websockets can map to multiple datachannels
	dcId := uuid.New().String()
	subLogger := h.logger.GetDatachannelLogger(dcId)

	h.logger.Infof("Creating new datachannel id: %v", dcId)

	// Build the actionParams to send to the datachannel to start the plugin
	actionParams := agms.DbActionParams{}

	actionParamsMarshalled, marshalErr := json.Marshal(actionParams)
	if marshalErr != nil {
		h.logger.Error(fmt.Errorf("error marshalling action params for kube"))
		return &datachannel.DataChannel{}, marshalErr
	}

	action = "db/" + action
	if datachannel, dcTmb, err := datachannel.New(subLogger, dcId, &h.tmb, websocket, h.refreshTokenCommand, h.configPath, action, actionParamsMarshalled); err != nil {
		h.logger.Error(err)
		return datachannel, err
	} else {

		// create a function to listen to the datachannel dying and then laugh
		go func() {
			for {
				select {
				case <-h.tmb.Dying():
					datachannel.Close(errors.New("db server closing"))
					return
				case <-dcTmb.Dying():
					// Wait until everything is dead and any close processes are sent before killing the datachannel
					dcTmb.Wait()

					// notify agent to close the datachannel
					h.logger.Info("Sending DataChannel Close")
					cdMessage := am.AgentMessage{
						ChannelId:   dcId,
						MessageType: string(am.CloseDataChannel),
					}
					h.websocket.Send(cdMessage)

					// close our websocket if the datachannel we closed was the last and it's not rest api
					if kube.KubeDaemonAction(action) != kube.RestApi && h.websocket.SubscriberCount() == 0 {
						h.websocket.Close(errors.New("all datachannels closed, closing websocket"))
					}
					return
				}
			}
		}()
		return datachannel, nil
	}
}
