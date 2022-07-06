package sshserver

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/ssh"
	"bastionzero.com/bctl/v1/bzerolib/bzio"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
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
	logger    *logger.Logger
	websocket *websocket.Websocket
	tmb       tomb.Tomb

	// Handler to select message types
	targetSelectHandler func(msg am.AgentMessage) (string, error)

	remoteHost   string
	remotePort   int
	targetUser   string
	identityFile string

	// fields for new datachannels
	params      map[string]string
	headers     map[string]string
	serviceUrl  string
	agentPubKey string
	cert        *bzcert.BZCert
}

func StartSshServer(
	logger *logger.Logger,
	targetUser string,
	dataChannelId string,
	cert *bzcert.BZCert,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	agentPubKey string,
	targetSelectHandler func(msg am.AgentMessage) (string, error),
	identityFile string,
	remoteHost string,
	remotePort int,
) error {

	server := &SshServer{
		logger:              logger,
		serviceUrl:          serviceUrl,
		targetUser:          targetUser,
		params:              params,
		headers:             headers,
		targetSelectHandler: targetSelectHandler,
		cert:                cert,
		agentPubKey:         agentPubKey,
		identityFile:        identityFile,
		remoteHost:          remoteHost,
		remotePort:          remotePort,
	}

	// Create a new websocket
	if err := server.newWebsocket(uuid.New().String()); err != nil {
		server.logger.Error(err)
		return err
	}

	// create our new datachannel
	if err := server.newDataChannel(string(bzssh.OpaqueSsh), server.websocket); err != nil {
		logger.Errorf("error starting datachannel: %s", err)
	}

	return nil
}

// for creating new websockets
func (s *SshServer) newWebsocket(wsId string) error {
	subLogger := s.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, s.serviceUrl, s.params, s.headers, s.targetSelectHandler, autoReconnect, getChallenge, websocket.Ssh); err != nil {
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

	s.logger.Infof("Creating new datachannel id: %s", dcId)

	pluginLogger := subLogger.GetPluginLogger(bzplugin.Ssh)

	plugin := ssh.New(pluginLogger, s.identityFile, bzio.OsFileIo{}, bzio.StdIo{})
	if err := plugin.StartAction(); err != nil {
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
	dc, dcTmb, err := datachannel.New(subLogger, dcId, &s.tmb, websocket, keysplitter, plugin, action, synPayload, attach, true)
	if err != nil {
		return err
	}

	// create a function to listen to the datachannel dying and then laugh
	go func() {
		for {
			select {
			case <-s.tmb.Dying():
				dc.Close(errors.New("ssh server closing"))
				return
			case <-dcTmb.Dead():
				if dcTmb.Err() != nil {
					// just take our innermost error to give the user
					errs := strings.Split(dcTmb.Err().Error(), ": ")
					errorString := fmt.Sprintf("error: %s\n", errs[len(errs)-1])
					os.Stdout.Write([]byte(errorString))
					os.Exit(1)
				} else {
					os.Exit(0)
				}
			}
		}
	}()
	return nil
}
