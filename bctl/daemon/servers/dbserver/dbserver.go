package dbserver

import (
	"fmt"
	"net"

	"github.com/google/uuid"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/db"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
	bzdb "bastionzero.com/bctl/v1/bzerolib/plugin/db"
)

const (
	// websocket connection parameters for all datachannels created by tcp server
	autoReconnect = true
	getChallenge  = false
)

type DbServer struct {
	logger   *logger.Logger
	doneChan chan error

	websocket   *websocket.Websocket
	tcpListener *net.TCPListener

	// Db specific vars
	remotePort int
	remoteHost string

	// fields for new datachannels
	localPort   string
	localHost   string
	agentPubKey string
	cert        *bzcert.DaemonBZCert
}

func StartDbServer(logger *logger.Logger,
	doneChan chan error,
	localPort string,
	localHost string,
	remotePort int,
	remoteHost string,
	cert *bzcert.DaemonBZCert,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	agentPubKey string,
	targetSelectHandler func(msg am.AgentMessage) (string, error),
) (*DbServer, error) {

	server := &DbServer{
		logger:      logger,
		doneChan:    doneChan,
		cert:        cert,
		localPort:   localPort,
		localHost:   localHost,
		remoteHost:  remoteHost,
		remotePort:  remotePort,
		agentPubKey: agentPubKey,
	}

	// Create a new websocket
	if err := server.newWebsocket(uuid.New().String(), serviceUrl, params, headers, targetSelectHandler); err != nil {
		return nil, fmt.Errorf("failed to create websocket: %s", err)
	}

	// Now create our local listener for TCP connections
	logger.Infof("Resolving TCP address for host:port %s:%s", localHost, localPort)
	localTcpAddress, err := net.ResolveTCPAddr("tcp", localHost+":"+localPort)
	if err != nil {
		server.websocket.Close(err)
		return nil, fmt.Errorf("failed to resolve TCP address %s", err)
	}

	logger.Infof("Setting up TCP listener")
	server.tcpListener, err = net.ListenTCP("tcp", localTcpAddress)
	if err != nil {
		server.websocket.Close(err)
		return nil, fmt.Errorf("failed to open local port to listen: %s", err)
	}

	// Do nothing with the first syn no-op call
	server.tcpListener.AcceptTCP()

	go server.handleConnections()

	return server, nil
}

func (d *DbServer) Shutdown(err error) {
	if d.websocket != nil {
		d.websocket.Close(err)
	}
	if d.tcpListener != nil {
		d.tcpListener.Close()
	}
	d.doneChan <- err
}

// for creating new websockets
func (d *DbServer) newWebsocket(wsId string, serviceUrl string, params map[string]string, headers map[string]string, targetSelectHandler func(msg am.AgentMessage) (string, error)) error {
	subLogger := d.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, serviceUrl, params, headers, targetSelectHandler, autoReconnect, getChallenge, websocket.Db); err != nil {
		return err
	} else {
		d.websocket = wsClient
		return nil
	}
}

func (d *DbServer) handleConnections() {
	// Block and keep listening for new tcp events
	for {
		conn, err := d.tcpListener.AcceptTCP()
		if err != nil {
			d.logger.Errorf("failed to accept connection: %s", err)
			continue
		}

		d.logger.Infof("Accepting new tcp connection")

		// create our new datachannel in its own go routine so that we can accept other tcp connections
		go func() {
			// every datachannel gets a uuid to distinguish it so a single websockets can map to multiple datachannels
			dcId := uuid.New().String()
			subLogger := d.logger.GetDatachannelLogger(dcId)
			pluginLogger := subLogger.GetPluginLogger(bzplugin.Db)
			plugin := db.New(pluginLogger)
			if err := plugin.StartAction(bzdb.Dial, conn); err != nil {
				d.logger.Errorf("error starting action: %s", err)
			} else if err := d.newDataChannel(dcId, string(bzdb.Dial), d.websocket, plugin); err != nil {
				d.logger.Errorf("error starting datachannel: %s", err)
			}
		}()
	}
}

// for creating new datachannels
func (d *DbServer) newDataChannel(dcId string, action string, websocket *websocket.Websocket, plugin *db.DbDaemonPlugin) error {
	subLogger := d.logger.GetDatachannelLogger(dcId)

	d.logger.Infof("Creating new datachannel id: %s", dcId)

	// Build the synPayload to send to the datachannel to start the plugin
	synPayload := bzdb.DbActionParams{
		RemotePort: d.remotePort,
		RemoteHost: d.remoteHost,
	}

	ksLogger := d.logger.GetComponentLogger("mrzap")
	keysplitter, err := keysplitting.New(ksLogger, d.agentPubKey, d.cert)
	if err != nil {
		return err
	}

	action = "db/" + action
	attach := false
	_, _, err = datachannel.New(subLogger, dcId, websocket, keysplitter, plugin, action, synPayload, attach, true)
	if err != nil {
		return err
	}
	return nil
}
