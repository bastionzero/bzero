package sshserver

import (
	"fmt"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/ssh"
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
	logger   *logger.Logger
	doneChan chan error

	websocket *websocket.Websocket
	dc        *datachannel.DataChannel
	dcTmb     *tomb.Tomb

	remoteHost string
	remotePort int
	localPort  string
	targetUser string

	identityFile   string
	knownHostsFile string
	hostNames      []string

	// fields for new datachannels
	agentPubKey string
	cert        *bzcert.DaemonBZCert
}

func StartSshServer(
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
	identityFile string,
	knownHostsFile string,
	hostNames []string,
	remoteHost string,
	remotePort int,
	localPort string,
	action string,
) *SshServer {

	server := &SshServer{
		logger:         logger,
		doneChan:       doneChan,
		targetUser:     targetUser,
		cert:           cert,
		agentPubKey:    agentPubKey,
		identityFile:   identityFile,
		knownHostsFile: knownHostsFile,
		hostNames:      hostNames,
		remoteHost:     remoteHost,
		remotePort:     remotePort,
		localPort:      localPort,
	}

	// Create a new websocket and datachannel
	if err := server.newWebsocket(uuid.New().String(), serviceUrl, params, headers, targetSelectHandler); err != nil {
		doneChan <- fmt.Errorf("failed to create websocket: %s", err)
		return nil
	} else if err := server.newDataChannel(action, server.websocket); err != nil {
		server.Shutdown(err)
		return nil
	}
	return server
}

func (s *SshServer) Shutdown(err error) {
	if s.websocket != nil {
		s.websocket.Close(err)
	}
	s.doneChan <- err
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
func (s *SshServer) newDataChannel(action string, websocket *websocket.Websocket) error {
	dcId := uuid.New().String()
	attach := false
	subLogger := s.logger.GetDatachannelLogger(dcId)
	var err error

	s.logger.Infof("Creating new datachannel id: %s", dcId)

	pluginLogger := subLogger.GetPluginLogger(bzplugin.Ssh)

	fileIo := bzio.OsFileIo{}

	idFile := bzssh.NewIdentityFile(s.identityFile, fileIo)
	khFile := bzssh.NewKnownHosts(s.knownHostsFile, s.hostNames, fileIo)

	plugin := ssh.New(pluginLogger, s.localPort, idFile, khFile, bzio.StdIo{})
	if err := plugin.StartAction(action); err != nil {
		return fmt.Errorf("failed to start action: %s", err)
	}

	synPayload := bzssh.SshActionParams{
		TargetUser: s.targetUser,
		RemoteHost: s.remoteHost,
		RemotePort: s.remotePort,
	}

	ksLogger := s.logger.GetComponentLogger("mrzap")
	keysplitter, err := keysplitting.New(ksLogger, s.agentPubKey, s.cert)
	if err != nil {
		return err
	}

	action = "ssh/" + action
	s.dc, s.dcTmb, err = datachannel.New(subLogger, dcId, websocket, keysplitter, plugin, action, synPayload, attach, false)
	if err != nil {
		return err
	}

	// listen for news that the datachannel has died
	go s.listenForDone()
	return nil
}

func (s *SshServer) listenForDone() {
	<-s.dcTmb.Dead()
	s.doneChan <- s.dcTmb.Err()
	return
}
