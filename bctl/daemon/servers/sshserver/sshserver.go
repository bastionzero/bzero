package sshserver

import (
	"encoding/json"
	"errors"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzssh "bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
	"github.com/google/uuid"
	"gopkg.in/tomb.v2"
)

const (
	// websocket connection parameters for all datachannels created by tcp server
	// FIXME: revisit whether autoreconnect should be true
	autoReconnect = false
	getChallenge  = false
)

type SshServer struct {
	logger    *logger.Logger
	websocket *websocket.Websocket
	tmb       tomb.Tomb

	// Handler to select message types
	targetSelectHandler func(msg am.AgentMessage) (string, error)

	// TODO: revisit
	targetUser   string
	identityFile string

	// fields for new datachannels
	params              map[string]string
	headers             map[string]string
	serviceUrl          string
	refreshTokenCommand string
	configPath          string
	agentPubKey         string
}

func StartSshServer(
	logger *logger.Logger,
	targetUser string,
	dataChannelId string,
	refreshTokenCommand string,
	configPath string,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	agentPubKey string,
	targetSelectHandler func(msg am.AgentMessage) (string, error),
	identityFile string) error {

	server := &SshServer{
		logger:              logger,
		serviceUrl:          serviceUrl,
		targetUser:          targetUser,
		params:              params,
		headers:             headers,
		targetSelectHandler: targetSelectHandler,
		configPath:          configPath,
		refreshTokenCommand: refreshTokenCommand,
		agentPubKey:         agentPubKey,
		identityFile:        identityFile,
	}

	// Create a new websocket
	if err := server.newWebsocket(uuid.New().String()); err != nil {
		server.logger.Error(err)
		return err
	}

	// create our new datachannel
	if _, err := server.newDataChannel(string(bzssh.DefaultSsh), server.websocket); err == nil {
	} else {
		logger.Errorf("error starting datachannel: %s", err)
	}

	return nil
}

// for creating new websockets
func (s *SshServer) newWebsocket(wsId string) error {
	subLogger := s.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, s.serviceUrl, s.params, s.headers, s.targetSelectHandler, autoReconnect, getChallenge, s.refreshTokenCommand, websocket.Ssh); err != nil {
		return err
	} else {
		s.websocket = wsClient
		return nil
	}
}

// for creating new datachannels
func (s *SshServer) newDataChannel(action string, websocket *websocket.Websocket) (*datachannel.DataChannel, error) {
	// every datachannel gets a uuid to distinguish it so a single websockets can map to multiple datachannels
	dcId := uuid.New().String()
	attach := false
	subLogger := s.logger.GetDatachannelLogger(dcId)

	s.logger.Infof("Creating new datachannel id: %s", dcId)

	// FIXME: why is there a message here even??
	actionParams := bzssh.SshActionParams{
		TargetUser:   s.targetUser,
		IdentityFile: s.identityFile,
	}

	actionParamsMarshalled, _ := json.Marshal(actionParams)

	action = "ssh/" + action
	// FIXME: params not lining up -- check another server...
	if dc, dcTmb, err := datachannel.New(subLogger, dcId, &s.tmb, websocket, s.refreshTokenCommand, s.configPath, action, actionParamsMarshalled, s.agentPubKey, attach); err != nil {
		s.logger.Error(err)
		return nil, err
	} else {

		// create a function to listen to the datachannel dying and then laugh
		go func() {
			for {
				select {
				case <-s.tmb.Dying():
					dc.Close(errors.New("ssh server closing"))
					return
				case <-dcTmb.Dying():
					// Wait until everything is dead and any close processes are sent before killing the datachannel
					dcTmb.Wait()

					// notify agent to close the datachannel
					s.logger.Info("Sending DataChannel Close")
					cdMessage := am.AgentMessage{
						ChannelId:   dcId,
						MessageType: string(am.CloseDataChannel),
					}
					s.websocket.Send(cdMessage)

					return
				}
			}
		}()
		return dc, nil
	}
}
