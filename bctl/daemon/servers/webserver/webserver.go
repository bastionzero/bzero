package webserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzweb "bastionzero.com/bctl/v1/bzerolib/plugin/web"
	"github.com/google/uuid"
	"gopkg.in/tomb.v2"
)

const (
	// websocket connection parameters for all datachannels created by tcp server
	autoReconnect = true
	getChallenge  = false
)

type WebServer struct {
	logger    *logger.Logger
	websocket *websocket.Websocket
	tmb       tomb.Tomb

	// Web connections only require a single datachannel
	datachannel *datachannel.DataChannel

	// Handler to select message types
	targetSelectHandler func(msg am.AgentMessage) (string, error)

	// Web specific vars
	// Either user the full dns (i.e. targetHostName) or the host:port
	targetPort int
	targetHost string

	// fields for new datachannels
	daemonport          string
	params              map[string]string
	headers             map[string]string
	serviceUrl          string
	refreshTokenCommand string
	configPath          string
}

func StartWebServer(logger *logger.Logger,
	daemonPort string,
	targetPort int,
	targetHost string,
	refreshTokenCommand string,
	configPath string,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	targetSelectHandler func(msg am.AgentMessage) (string, error)) error {

	listener := &WebServer{
		logger:              logger,
		serviceUrl:          serviceUrl,
		params:              params,
		headers:             headers,
		targetSelectHandler: targetSelectHandler,
		configPath:          configPath,
		refreshTokenCommand: refreshTokenCommand,
		daemonport:          daemonPort,
		targetHost:          targetHost,
		targetPort:          targetPort,
	}

	// Create a new websocket
	if err := listener.newWebsocket(uuid.New().String()); err != nil {
		listener.logger.Error(err)
		return err
	}

	// Create a single datachannel for all of our db calls
	if datachannel, err := listener.newDataChannel(string(bzweb.Dial), listener.websocket); err == nil {
		listener.datachannel = datachannel
	} else {
		return err
	}

	// Now create our local listener for TCP connections
	logger.Infof("Resolving TCP address for port: %s", daemonPort)
	localTcpAddress, err := net.ResolveTCPAddr("tcp", ":"+daemonPort)
	if err != nil {
		logger.Errorf("Failed to resolve TCP address %s", err)
		os.Exit(1)
	}

	logger.Infof("Setting up TCP lister")
	localTcpListener, err := net.ListenTCP("tcp", localTcpAddress)
	if err != nil {
		logger.Errorf("Failed to open local port to listen: %s", err)
		os.Exit(1)
	}

	// Always ensure we close the local tcp connection when we exit
	defer localTcpListener.Close()

	// Wait until the datachannel is ready, block here?
	// TODO: This

	// Block and keep listening for new tcp events
	for {
		conn, err := localTcpListener.AcceptTCP()
		if err != nil {
			logger.Errorf("Failed to accept connection '%s'", err)
			continue
		}

		// Always generate a requestId, each new proxy connection is its own request
		requestId := uuid.New().String()

		go listener.handleProxy(conn, logger, requestId)
	}
}

func (h *WebServer) handleProxy(lconn *net.TCPConn, logger *logger.Logger, requestId string) {
	food := bzweb.WebFood{
		Action: bzweb.Dial,
		Conn:   lconn,
	}
	// Start the dial plugin
	h.datachannel.Feed(food)
}

// for creating new websockets
func (h *WebServer) newWebsocket(wsId string) error {
	subLogger := h.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, wsId, h.serviceUrl, h.params, h.headers, h.targetSelectHandler, autoReconnect, getChallenge, h.refreshTokenCommand, websocket.Web); err != nil {
		return err
	} else {
		h.websocket = wsClient
		return nil
	}
}

// for creating new datachannels
func (h *WebServer) newDataChannel(action string, websocket *websocket.Websocket) (*datachannel.DataChannel, error) {
	// every datachannel gets a uuid to distinguish it so a single websockets can map to multiple datachannels
	dcId := uuid.New().String()
	subLogger := h.logger.GetDatachannelLogger(dcId)

	h.logger.Infof("Creating new datachannel id: %v", dcId)

	// Build the actionParams to send to the datachannel to start the plugin
	actionParams := bzweb.WebActionParams{
		RemotePort: h.targetPort,
		RemoteHost: h.targetHost,
	}

	actionParamsMarshalled, marshalErr := json.Marshal(actionParams)
	if marshalErr != nil {
		h.logger.Error(fmt.Errorf("error marshalling action params for web"))
		return &datachannel.DataChannel{}, marshalErr
	}

	action = "web/" + action
	if datachannel, dcTmb, err := datachannel.New(subLogger, dcId, &h.tmb, websocket, h.refreshTokenCommand, h.configPath, action, actionParamsMarshalled); err != nil {
		h.logger.Error(err)
		return datachannel, err
	} else {

		// create a function to listen to the datachannel dying and then laugh
		go func() {
			for {
				select {
				case <-h.tmb.Dying():
					datachannel.Close(errors.New("web server closing"))
					return
				case <-dcTmb.Dying():
					// Wait until everything is dead and any close processes are sent before killing the datachannel
					dcTmb.Wait()

					// notify agent to close the datachannel
					h.logger.Info("Sending DataChannel Close")
					cdMessage := am.AgentMessage{
						ChannelId:   dcId,
						MessageType: string(am.CloseDataChannel),
					}
					h.websocket.Send(cdMessage)

					// close our websocket
					h.websocket.Close(errors.New("all datachannels closed, closing websocket"))
					return
				}
			}
		}()
		return datachannel, nil
	}
}
