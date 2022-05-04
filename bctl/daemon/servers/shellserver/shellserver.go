package shellserver

import (
	"encoding/json"
	"errors"
	"os"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzshell "bastionzero.com/bctl/v1/bzerolib/plugin/shell"
	"github.com/google/uuid"
	"gopkg.in/tomb.v2"
)

const (
	// websocket connection parameters for all datachannels created by tcp server
	autoReconnect = false
	getChallenge  = false
)

type ShellServer struct {
	logger    *logger.Logger
	websocket *websocket.Websocket
	tmb       tomb.Tomb

	// Handler to select message types
	targetSelectHandler func(msg am.AgentMessage) (string, error)

	// Shell specific vars
	targetUser    string
	dataChannelId string

	// fields for new datachannels
	params              map[string]string
	headers             map[string]string
	serviceUrl          string
	refreshTokenCommand string
	configPath          string
	agentPubKey         string
}

func StartShellServer(
	logger *logger.Logger,
	targetUser string,
	dataChannelId string,
	refreshTokenCommand string,
	configPath string,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	agentPubKey string,
	targetSelectHandler func(msg am.AgentMessage) (string, error)) error {

	shellServer := &ShellServer{
		logger:              logger,
		serviceUrl:          serviceUrl,
		params:              params,
		headers:             headers,
		targetSelectHandler: targetSelectHandler,
		configPath:          configPath,
		refreshTokenCommand: refreshTokenCommand,
		targetUser:          targetUser,
		dataChannelId:       dataChannelId,
		agentPubKey:         agentPubKey,
	}

	// Create a new websocket
	if err := shellServer.newWebsocket(uuid.New().String()); err != nil {
		shellServer.logger.Error(err)
		return err
	}

	// create our new datachannel
	if _, err := shellServer.newDataChannel(string(bzshell.DefaultShell), shellServer.websocket); err == nil {
	} else {
		logger.Errorf("error starting datachannel: %s", err)
	}

	return nil
}

// for creating new websockets
func (ss *ShellServer) newWebsocket(wsId string) error {
	subLogger := ss.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, ss.serviceUrl, ss.params, ss.headers, ss.targetSelectHandler, autoReconnect, getChallenge, ss.refreshTokenCommand, websocket.Shell); err != nil {
		return err
	} else {
		ss.websocket = wsClient
		return nil
	}
}

// for creating new datachannels
func (ss *ShellServer) newDataChannel(action string, websocket *websocket.Websocket) (*datachannel.DataChannel, error) {
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

	// Build the action payload to send in the syn message when opening the datachannel
	// FIXME: why is this an openMessage and not ActionParams?
	actionParams := bzshell.ShellOpenMessage{
		TargetUser: ss.targetUser,
	}
	actionParamsMarshalled, _ := json.Marshal(actionParams)

	action = "shell/" + action
	if dc, dcTmb, err := datachannel.New(subLogger, ss.dataChannelId, &ss.tmb, websocket, ss.refreshTokenCommand, ss.configPath, action, actionParamsMarshalled, ss.agentPubKey, attach); err != nil {
		ss.logger.Error(err)
		return nil, err
	} else {

		// create a function to listen to the datachannel dying and then exit the shell daemon process
		go func() {
			for {
				select {
				case <-ss.tmb.Dying():
					dc.Close(errors.New("shell server exiting...closing datachannel"))
					return
				case <-dcTmb.Dying():
					// Wait until everything is dead and any close processes are sent before killing the datachannel
					dcTmb.Wait()

					errorCode := 1
					os.Exit(errorCode)
				}
			}
		}()
		return dc, nil
	}
}
