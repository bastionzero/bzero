package sshserver

import (
	"fmt"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/ssh"
	"bastionzero.com/bctl/v1/bctl/daemon/servers"
	"bastionzero.com/bctl/v1/bzerolib/bzio"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	"github.com/google/uuid"
	"gopkg.in/tomb.v2"
)

const (
	// websocket connection parameters for all datachannels created by tcp server
	autoReconnect = false
	getChallenge  = false
)

type SshServer struct {
	logger             *logger.Logger
	daemonShutdownChan chan struct{}
	doneChan           chan error

	websocket *websocket.Websocket
	dc        *datachannel.DataChannel
	dcTmb     *tomb.Tomb
}

func StartSshServer(
	logger *logger.Logger,
	daemonShutdownChan chan struct{},
	doneChan chan error,
	targetUser string,
	dataChannelId string,
	cert *bzcert.DaemonBZCert,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	agentPubKey string,
	targetSelectHandler func(msg am.AgentMessage) (string, error),
	identityFile string,
	knownHostsFile string,
	hostNames []string,
	remoteHost string,
	remotePort int,
	localPort string,
	action string,
) {

	server := &SshServer{
		logger:             logger,
		daemonShutdownChan: daemonShutdownChan,
		doneChan:           doneChan,
	}

	// Create a new websocket and datachannel
	if err := server.newWebsocket(uuid.New().String(), serviceUrl, params, headers, targetSelectHandler); err != nil {
		doneChan <- fmt.Errorf("failed to create websocket: %s", err)
	} else {
		fileIo := bzio.OsFileIo{}

		idFile := bzssh.NewIdentityFile(identityFile, fileIo)
		khFile := bzssh.NewKnownHosts(knownHostsFile, hostNames, fileIo)

		synPayload := bzssh.SshActionParams{
			TargetUser: targetUser,
			RemoteHost: remoteHost,
			RemotePort: remotePort,
		}

		ksLogger := server.logger.GetComponentLogger("mrzap")
		keysplitter, err := keysplitting.New(ksLogger, agentPubKey, cert)
		if err != nil {
			server.Shutdown(err)
			return
		}

		if err := server.newDataChannel(server.websocket, action, idFile, khFile, localPort, synPayload, keysplitter); err != nil {
			server.Shutdown(fmt.Errorf("failed to create datachannel: %s", err))
			return
		}
	}
}

func (s *SshServer) DaemonShutdownChan() chan struct{} {
	return s.daemonShutdownChan
}

func (s *SshServer) Shutdown(err error) {
	if s.websocket != nil {
		s.websocket.Close(err)
	}
	s.doneChan <- err
}

func (s *SshServer) DataChannelTomb() *tomb.Tomb {
	return s.dcTmb
}

func (s *SshServer) DoneChan() chan error {
	return s.doneChan
}

// for creating new websockets
func (s *SshServer) newWebsocket(wsId string, serviceUrl string, params map[string]string, headers map[string]string, targetSelectHandler func(msg am.AgentMessage) (string, error)) error {
	subLogger := s.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, serviceUrl, params, headers, targetSelectHandler, autoReconnect, getChallenge, websocket.Ssh); err != nil {
		return err
	} else {
		s.websocket = wsClient
		return nil
	}
}

// for creating new datachannels
func (s *SshServer) newDataChannel(websocket *websocket.Websocket, action string, idFile bzssh.IIdentityFile, khFile bzssh.IKnownHosts, localPort string, synPayload bzssh.SshActionParams, keysplitter *keysplitting.Keysplitting) error {
	dcId := uuid.New().String()
	attach := false
	subLogger := s.logger.GetDatachannelLogger(dcId)
	var err error

	s.logger.Infof("Creating new datachannel id: %s", dcId)

	pluginLogger := subLogger.GetPluginLogger(bzplugin.Ssh)

	plugin := ssh.New(pluginLogger, localPort, idFile, khFile, bzio.StdIo{})
	if err := plugin.StartAction(action); err != nil {
		return fmt.Errorf("failed to start action: %s", err)
	}

	action = "ssh/" + action
	s.dc, s.dcTmb, err = datachannel.New(subLogger, dcId, websocket, keysplitter, plugin, action, synPayload, attach, false)
	if err != nil {
		return err
	}

	// listen for shutdown orders from the daemon or news that the datachannel has died
	go servers.CoolEphemeralServerFunc(s)
	return nil
}
