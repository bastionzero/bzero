package dbserver

import (
	"errors"
	"net"
	"os"

	"github.com/google/uuid"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/exitcodes"
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
	logger    *logger.Logger
	websocket *websocket.Websocket
	tmb       tomb.Tomb

	// Db specific vars
	remotePort int
	remoteHost string

	// fields for new datachannels
	localPort   string
	localHost   string
	params      map[string]string
	headers     map[string]string
	serviceUrl  string
	agentPubKey string
	cert        *bzcert.DaemonBZCert
}

func StartDbServer(logger *logger.Logger,
	localPort string,
	localHost string,
	remotePort int,
	remoteHost string,
	cert *bzcert.DaemonBZCert,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	agentPubKey string,
) error {

	server := &DbServer{
		logger:      logger,
		serviceUrl:  serviceUrl,
		params:      params,
		headers:     headers,
		cert:        cert,
		localPort:   localPort,
		localHost:   localHost,
		remoteHost:  remoteHost,
		remotePort:  remotePort,
		agentPubKey: agentPubKey,
	}

	// Create a new websocket
	if err := server.newWebsocket(uuid.New().String()); err != nil {
		server.logger.Error(err)
		return err
	}

	// Now create our local listener for TCP connections
	logger.Infof("Resolving TCP address for host:port %s:%s", localHost, localPort)
	localTcpAddress, err := net.ResolveTCPAddr("tcp", localHost+":"+localPort)
	if err != nil {
		logger.Errorf("Failed to resolve TCP address %s", err)
		os.Exit(exitcodes.UNSPECIFIED_ERROR)
	}

	logger.Infof("Setting up TCP listener")
	localTcpListener, err := net.ListenTCP("tcp", localTcpAddress)
	if err != nil {
		logger.Errorf("Failed to open local port to listen: %s", err)
		os.Exit(exitcodes.UNSPECIFIED_ERROR)
	}

	// Always ensure we close the local tcp connection when we exit
	defer localTcpListener.Close()

	// Do nothing with the first syn no-op call
	localTcpListener.AcceptTCP()

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
			// every datachannel gets a uuid to distinguish it so a single websockets can map to multiple datachannels
			dcId := uuid.New().String()
			subLogger := logger.GetDatachannelLogger(dcId)
			pluginLogger := subLogger.GetPluginLogger(bzplugin.Db)
			plugin := db.New(pluginLogger)
			if err := plugin.StartAction(bzdb.Dial, conn); err != nil {
				logger.Errorf("error starting action: %s", err)
			} else if err := server.newDataChannel(dcId, string(bzdb.Dial), server.websocket, plugin); err != nil {
				logger.Errorf("error starting datachannel: %s", err)
			}
		}()
	}
}

// for creating new websockets
func (d *DbServer) newWebsocket(wsId string) error {
	subLogger := d.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, d.serviceUrl, d.params, d.headers, autoReconnect, getChallenge, websocket.Db); err != nil {
		return err
	} else {
		d.websocket = wsClient
		return nil
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
	dc, dcTmb, err := datachannel.New(subLogger, dcId, &d.tmb, websocket, keysplitter, plugin, action, synPayload, attach, true)
	if err != nil {
		return err
	}

	// create a function to listen to the datachannel dying and then laugh
	go func() {
		for {
			select {
			// TODO: this is redundant and/or not used -- we should take a dedicated look at daemon shutdown procedure
			case <-d.tmb.Dying():
				dc.Close(errors.New("db server closing"))
				return
			case <-dcTmb.Dead():
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
	return nil
}
