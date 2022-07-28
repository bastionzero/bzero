package webserver

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/web"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
	bzweb "bastionzero.com/bctl/v1/bzerolib/plugin/web"
)

const (
	// websocket connection parameters for all datachannels created by tcp server
	autoReconnect = true
	getChallenge  = false

	// TODO: make these easily configurable values
	maxRequestSize = 10 * 1024 * 1024  // 10MB
	maxFileUpload  = 151 * 1024 * 1024 // 151MB a little extra for request fluff
)

type WebServer struct {
	logger   *logger.Logger
	doneChan chan error

	websocket *websocket.Websocket

	// Web specific vars
	// Either user the full dns (i.e. targetHostName) or the host:port
	targetPort int
	targetHost string

	// fields for new datachannels
	localPort   string
	localHost   string
	agentPubKey string
	cert        *bzcert.DaemonBZCert
}

func StartWebServer(logger *logger.Logger,
	doneChan chan error,
	localPort string,
	localHost string,
	targetPort int,
	targetHost string,
	cert *bzcert.DaemonBZCert,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	agentPubKey string,
	targetSelectHandler func(msg am.AgentMessage) (string, error),
) (*WebServer, error) {

	server := &WebServer{
		logger:      logger,
		doneChan:    doneChan,
		cert:        cert,
		localPort:   localPort,
		localHost:   localHost,
		targetHost:  targetHost,
		targetPort:  targetPort,
		agentPubKey: agentPubKey,
	}

	// Create a new websocket
	if err := server.newWebsocket(uuid.New().String(), serviceUrl, params, headers, targetSelectHandler); err != nil {
		return nil, fmt.Errorf("failed to create websocket: %s", err)
	}

	// Create HTTP Server listens for incoming kubectl commands
	go func() {
		// Define our http handlers
		// library will automatically put each call in its own thread
		http.HandleFunc("/", server.capRequestSize(server.handleHttp))

		if err := http.ListenAndServe(fmt.Sprintf("%s:%s", localHost, localPort), nil); err != nil {
			logger.Error(err)
		}
	}()
	return server, nil
}

func (w *WebServer) Shutdown(err error) {
	if w.websocket != nil {
		w.websocket.Close(err)
	}
	w.doneChan <- err
}

// this function operates as middleware between the http handler and the handleHttp call below
// it checks to see if someone is trying to send a request body that is far too large
func (w *WebServer) capRequestSize(h http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart") {
			if request.ContentLength > maxFileUpload {
				// for multipart/form-data type requests, the request body won't exceed our maximum single request size
				// but we still want to cap the size of uploads because they are stored in their entirety on the target.
				// Not optimal, here's the ticket: CWC-1647
				// We shouldn't be relying on content length too much since it can be modified to be whatever.
				rerr := "BastionZero: Request is too large. Maximum upload is 150MB"
				w.logger.Errorf(rerr)
				http.Error(writer, rerr, http.StatusRequestEntityTooLarge)
				return
			}
		} else {
			request.Body = http.MaxBytesReader(writer, request.Body, maxRequestSize)
			if err := request.ParseForm(); err != nil {
				rerr := "BastionZero: Request is too large. Maximum request size is 10MB"
				w.logger.Errorf(rerr)
				http.Error(writer, rerr, http.StatusRequestEntityTooLarge)
				return
			}
		}

		h(writer, request)
	}
}

func (w *WebServer) handleHttp(writer http.ResponseWriter, request *http.Request) {
	// every datachannel gets a uuid to distinguish it so a single websockets can map to multiple datachannels
	dcId := uuid.New().String()

	// create our new plugin and datachannel
	subLogger := w.logger.GetDatachannelLogger(dcId)
	subLogger = subLogger.GetPluginLogger(bzplugin.Web)
	plugin := web.New(subLogger, w.targetHost, w.targetPort)

	action := bzweb.Dial
	// This will work for http 1.1 and that is what we need to support
	// Ref: https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Upgrade
	// Ref: https://datatracker.ietf.org/doc/html/rfc6455#section-1.7
	isWebsocketRequest := request.Header.Get("Upgrade")
	if isWebsocketRequest == "websocket" {
		action = bzweb.Websocket
	}

	if err := w.newDataChannel(dcId, action, w.websocket, plugin); err != nil {
		w.logger.Errorf("error starting datachannel: %s", err)
	}
	if err := plugin.StartAction(action, writer, request); err != nil {
		w.logger.Errorf("error starting action: %s", err)
	}
}

// for creating new websockets
func (w *WebServer) newWebsocket(wsId string, serviceUrl string, params map[string]string, headers map[string]string, targetSelectHandler func(msg am.AgentMessage) (string, error)) error {
	subLogger := w.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, serviceUrl, params, headers, targetSelectHandler, autoReconnect, getChallenge, websocket.Web); err != nil {
		return err
	} else {
		w.websocket = wsClient
		return nil
	}
}

// for creating new datachannels
func (w *WebServer) newDataChannel(dcId string, action bzweb.WebAction, websocket *websocket.Websocket, plugin *web.WebDaemonPlugin) error {
	attach := false
	subLogger := w.logger.GetDatachannelLogger(dcId)

	w.logger.Infof("Creating new datachannel for web with id: %s", dcId)

	// Build the actionParams to send to the datachannel to start the plugin
	synPayload := bzweb.WebActionParams{
		RemotePort: w.targetPort,
		RemoteHost: w.targetHost,
	}

	ksLogger := w.logger.GetComponentLogger("mrzap")
	keysplitter, err := keysplitting.New(ksLogger, w.agentPubKey, w.cert)
	if err != nil {
		return err
	}

	actString := "web/" + string(action)
	_, _, err = datachannel.New(subLogger, dcId, websocket, keysplitter, plugin, actString, synPayload, attach, true)
	if err != nil {
		return err
	}
	return nil
}
