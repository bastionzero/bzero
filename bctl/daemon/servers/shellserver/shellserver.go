package shellserver

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/exitcodes"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/shell"
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
	logger    *logger.Logger
	websocket *websocket.Websocket
	tmb       tomb.Tomb

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
	cert                *bzcert.DaemonBZCert
}

func StartShellServer(
	logger *logger.Logger,
	targetUser string,
	dataChannelId string,
	cert *bzcert.DaemonBZCert,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	agentPubKey string,
) error {

	server := &ShellServer{
		logger:        logger,
		serviceUrl:    serviceUrl,
		params:        params,
		headers:       headers,
		cert:          cert,
		targetUser:    targetUser,
		dataChannelId: dataChannelId,
		agentPubKey:   agentPubKey,
	}

	// Create a new websocket
	if err := server.newWebsocket(uuid.New().String()); err != nil {
		server.logger.Error(err)
		return err
	}

	// create our new datachannel
	if err := server.newDataChannel(string(bzshell.DefaultShell), server.websocket); err != nil {
		logger.Errorf("error starting datachannel: %s", err)
		os.Exit(exitcodes.UNSPECIFIED_ERROR)
	}

	return nil
}

// for creating new websockets
func (ss *ShellServer) newWebsocket(wsId string) error {
	subLogger := ss.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, ss.serviceUrl, ss.params, ss.headers, autoReconnect, getChallenge, websocket.Shell); err != nil {
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
	dc, dcTmb, err := datachannel.New(subLogger, ss.dataChannelId, &ss.tmb, websocket, keysplitter, plugin, action, synPayload, attach, false)
	if err != nil {
		return err
	}

	// create a function to listen to the datachannel dying and then exit the shell daemon process
	go func() {
		for {
			select {
			case <-ss.tmb.Dying():
				dc.Close(errors.New("shell server exiting...closing datachannel"))
				return
			case <-dcTmb.Dead():
				// bubble up our error to the user
				if err := dcTmb.Err(); err != nil {

					// Handle custom daemon exit codes which will be reported by zli
					exitcodes.HandleDaemonError(err, ss.logger)

					// otherwise bubble up the error to stdout
					// let's just take our innermost error to give the user
					errs := strings.Split(err.Error(), ": ")
					errorString := fmt.Sprintf("error: %s", errs[len(errs)-1])
					os.Stdout.Write([]byte(errorString))
					os.Exit(exitcodes.UNSPECIFIED_ERROR)
				} else {
					os.Exit(exitcodes.SUCCESS)
				}
			}
		}
	}()
	return nil
}
