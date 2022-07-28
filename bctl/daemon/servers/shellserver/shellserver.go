package shellserver

import (
	"fmt"

	"github.com/google/uuid"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/shell"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
)

const (
	// websocket connection parameters for all datachannels created by tcp server
	autoReconnect = false
	getChallenge  = false
)

type ShellServer struct {
	logger   *logger.Logger
	doneChan chan error

	websocket *websocket.Websocket
	dc        *datachannel.DataChannel
	dcTmb     *tomb.Tomb

	// Shell specific vars
	targetUser    string
	dataChannelId string

	// fields for new datachannels
	agentPubKey string
	cert        *bzcert.DaemonBZCert
}

func StartShellServer(
	logger *logger.Logger,
	doneChan chan error,
	targetUser string,
	dataChannelId string,
	cert *bzcert.DaemonBZCert,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	agentPubKey string,
	targetSelectHandler func(msg am.AgentMessage) (string, error),
) (*ShellServer, error) {

	server := &ShellServer{
		logger:        logger,
		doneChan:      doneChan,
		cert:          cert,
		targetUser:    targetUser,
		dataChannelId: dataChannelId,
		agentPubKey:   agentPubKey,
	}

	// Create a new websocket and datachannel
	if err := server.newWebsocket(uuid.New().String(), serviceUrl, params, headers, targetSelectHandler); err != nil {
		return nil, fmt.Errorf("failed to create websocket: %s", err)
	} else if err := server.newDataChannel(string(bzshell.DefaultShell), server.websocket); err != nil {
		server.websocket.Close(err)
		return nil, fmt.Errorf("failed to create datachannel: %s", err)
	}
	return server, nil
}

func (ss *ShellServer) Shutdown(err error) {
	if ss.websocket != nil {
		ss.websocket.Close(err)
	}
	ss.doneChan <- err
}

// for creating new websockets
func (ss *ShellServer) newWebsocket(wsId string, serviceUrl string, params map[string]string, headers map[string]string, targetSelectHandler func(msg am.AgentMessage) (string, error)) error {
	subLogger := ss.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, serviceUrl, params, headers, targetSelectHandler, autoReconnect, getChallenge, websocket.Shell); err != nil {
		return err
	} else {
		ss.websocket = wsClient
		return nil
	}
}

// for creating new datachannels
func (ss *ShellServer) newDataChannel(action string, websocket *websocket.Websocket) error {
	var attach bool
	if ss.dataChannelId == "" {
		ss.dataChannelId = uuid.New().String()
		attach = false
		ss.logger.Infof("Creating new datachannel id: %s", ss.dataChannelId)
	} else {
		attach = true
		ss.logger.Infof("Attaching to an existing datachannel id: %s", ss.dataChannelId)
	}

	// every datachannel gets a uuid to distinguish it so a single websockets can map to multiple datachannels
	subLogger := ss.logger.GetDatachannelLogger(ss.dataChannelId)

	// create our plugin and start the action
	pluginLogger := subLogger.GetPluginLogger(bzplugin.Shell)
	plugin := shell.New(pluginLogger)
	if err := plugin.StartAction(attach); err != nil {
		return fmt.Errorf("failed to start action: %s", err)
	}

	// Build the action payload to send in the syn message when opening the datachannel
	synPayload := bzshell.ShellActionParams{
		TargetUser: ss.targetUser,
	}

	ksLogger := ss.logger.GetComponentLogger("mrzap")
	keysplitter, err := keysplitting.New(ksLogger, ss.agentPubKey, ss.cert)
	if err != nil {
		return err
	}

	action = "shell/" + action
	ss.dc, ss.dcTmb, err = datachannel.New(subLogger, ss.dataChannelId, websocket, keysplitter, plugin, action, synPayload, attach, false)
	if err != nil {
		return err
	}

	// listen for news that the datachannel has died
	go ss.listenForDatachannelDone()
	return nil
}

func (ss *ShellServer) listenForDatachannelDone() {
	<-ss.dcTmb.Dead()
	ss.Shutdown(ss.dcTmb.Err())
}
