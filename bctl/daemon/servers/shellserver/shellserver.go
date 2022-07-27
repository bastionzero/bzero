package shellserver

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/shell"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
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
	logger     *logger.Logger
	connection *websocket.Websocket
	tmb        tomb.Tomb

	// Shell specific vars
	targetUser    string
	dataChannelId string

	// fields for new datachannels
	configPath  string
	agentPubKey string
	cert        *bzcert.BZCert
}

func StartShellServer(
	logger *logger.Logger,
	targetUser string,
	dataChannelId string,
	cert *bzcert.BZCert,
	serviceUrl string,
	connUrl string,
	params map[string][]string,
	headers map[string][]string,
	agentPubKey string,
) error {

	server := &ShellServer{
		logger:        logger,
		cert:          cert,
		targetUser:    targetUser,
		dataChannelId: dataChannelId,
		agentPubKey:   agentPubKey,
	}

	// Create our one connection in the form of a websocket
	subLogger := logger.GetWebsocketLogger(uuid.New().String())
	if client, err := websocket.New(subLogger, serviceUrl, connUrl, params, headers, autoReconnect, websocket.DaemonWebsocket); err != nil {
		return err
	} else {
		server.connection = client
	}

	// create our new datachannel
	if err := server.newDataChannel(string(bzshell.DefaultShell), server.connection); err != nil {
		logger.Errorf("error starting datachannel: %s", err)
	}

	return nil
}

// for creating new datachannels
func (ss *ShellServer) newDataChannel(action string, connection *websocket.Websocket) error {
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
	dc, dcTmb, err := datachannel.New(subLogger, ss.dataChannelId, &ss.tmb, connection, keysplitter, plugin, action, synPayload, attach, true)
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
				if dcTmb.Err() != nil {
					// let's just take our innermost error to give the user
					errs := strings.Split(dcTmb.Err().Error(), ": ")
					errorString := fmt.Sprintf("error: %s", errs[len(errs)-1])
					os.Stdout.Write([]byte(errorString))
				}
				errorCode := 1
				os.Exit(errorCode)
			}
		}
	}()
	return nil
}
