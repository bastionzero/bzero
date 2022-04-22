package dbserver

import (
	"encoding/json"
	"errors"
	"net"
	"os"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzdb "bastionzero.com/bctl/v1/bzerolib/plugin/db"
	"github.com/google/uuid"
	"gopkg.in/tomb.v2"
)

const (
	// websocket connection parameters for all datachannels created by tcp server
	autoReconnect = true
	getChallenge  = false
)

type DbServer struct {
	logger    *logger.Logger
	websocket *websocket.Websocket
	tmb       tomb.Tomb

	// Handler to select message types
	targetSelectHandler func(msg am.AgentMessage) (string, error)

	// Db specific vars
	remotePort int
	remoteHost string

	// fields for new datachannels
	localPort           string
	localHost           string
	params              map[string]string
	headers             map[string]string
	serviceUrl          string
	refreshTokenCommand string
	configPath          string
	agentPubKey         string
}

func StartDbServer(logger *logger.Logger,
	localPort string,
	localHost string,
	remotePort int,
	remoteHost string,
	refreshTokenCommand string,
	configPath string,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	agentPubKey string,
	targetSelectHandler func(msg am.AgentMessage) (string, error)) error {

	listener := &DbServer{
		logger:              logger,
		serviceUrl:          serviceUrl,
		params:              params,
		headers:             headers,
		targetSelectHandler: targetSelectHandler,
		configPath:          configPath,
		refreshTokenCommand: refreshTokenCommand,
		localPort:           localPort,
		localHost:           localHost,
		remoteHost:          remoteHost,
		remotePort:          remotePort,
		agentPubKey:         agentPubKey,
	}

	// Create a new websocket
	if err := listener.newWebsocket(uuid.New().String()); err != nil {
		listener.logger.Error(err)
		return err
	}

	// Now create our local listener for TCP connections
	logger.Infof("Resolving TCP address for host:port %s:%s", localHost, localPort)
	localTcpAddress, err := net.ResolveTCPAddr("tcp", localHost+":"+localPort)
	if err != nil {
		logger.Errorf("Failed to resolve TCP address %s", err)
		os.Exit(1)
	}

	logger.Infof("Setting up TCP listener")
	localTcpListener, err := net.ListenTCP("tcp", localTcpAddress)
	if err != nil {
		logger.Errorf("Failed to open local port to listen: %s", err)
		os.Exit(1)
	}

	// Always ensure we close the local tcp connection when we exit
	defer localTcpListener.Close()

	// Block and keep listening for new tcp events
	for {
		conn, err := localTcpListener.AcceptTCP()
		if err != nil {
			logger.Errorf("failed to accept connection: %s", err)
			continue
		}

		logger.Infof("Accepting new tcp connection")

		// create our new datachannel in its own go routine so that we can accept other tcp connections
		go func() {
			if dc, err := listener.newDataChannel(string(bzdb.Dial), listener.websocket); err == nil {

				// Start the dial plugin
				food := bzdb.DbFood{
					Action: bzdb.Dial,
					Conn:   conn,
				}
				dc.Feed(food)
			} else {
				logger.Errorf("error starting datachannel: %s", err)
			}
		}()
	}
}

// for creating new websockets
func (d *DbServer) newWebsocket(wsId string) error {
	subLogger := d.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, d.serviceUrl, d.params, d.headers, d.targetSelectHandler, autoReconnect, getChallenge, d.refreshTokenCommand, websocket.Db); err != nil {
		return err
	} else {
		d.websocket = wsClient
		return nil
	}
}

// for creating new datachannels
func (d *DbServer) newDataChannel(action string, websocket *websocket.Websocket) (*datachannel.DataChannel, error) {
	// every datachannel gets a uuid to distinguish it so a single websockets can map to multiple datachannels
	dcId := uuid.New().String()
	attach := false
	subLogger := d.logger.GetDatachannelLogger(dcId)

	d.logger.Infof("Creating new datachannel id: %s", dcId)

	// Build the actionParams to send to the datachannel to start the plugin
	actionParams := bzdb.DbActionParams{
		RemotePort: d.remotePort,
		RemoteHost: d.remoteHost,
	}
	actionParamsMarshalled, _ := json.Marshal(actionParams)

	action = "db/" + action
	if dc, dcTmb, err := datachannel.New(subLogger, dcId, &d.tmb, websocket, d.refreshTokenCommand, d.configPath, action, actionParamsMarshalled, d.agentPubKey, attach); err != nil {
		d.logger.Error(err)
		return nil, err
	} else {

		// create a function to listen to the datachannel dying and then laugh
		go func() {
			for {
				select {
				case <-d.tmb.Dying():
					dc.Close(errors.New("db server closing"))
					return
				case <-dcTmb.Dying():
					// Wait until everything is dead and any close processes are sent before killing the datachannel
					dcTmb.Wait()

					// notify agent to close the datachannel
					d.logger.Info("Sending DataChannel Close")
					cdMessage := am.AgentMessage{
						ChannelId:   dcId,
						MessageType: string(am.CloseDataChannel),
					}
					d.websocket.Send(cdMessage)

					return
				}
			}
		}()
		return dc, nil
	}
}
