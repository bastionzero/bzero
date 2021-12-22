package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube"
	kubeutils "bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/utils"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

const (
	// This token is used when validating our Bearer token. Our token comes in with the form "{localhostToken}++++{english command i.e. zli kube get pods}++++{logId}"
	// The english command and logId are only generated if the user is using "zli kube ..."
	// So we use this securityTokenDelimiter to split up our token and extract what might be there
	securityTokenDelimiter = "++++"

	// websocket connection parameters for all datachannels created by http server
	autoReconnect = true
	getChallenge  = false
)

type StatusMessage struct {
	ExitMessage string `json:"ExitMessage"`
}

type HTTPServer struct {
	logger      *logger.Logger
	websocket   *websocket.Websocket // TODO: This will need to be a dictionary for when we have multiple
	tmb         tomb.Tomb
	exitMessage string
	ready       bool

	// RestApi is a special case where we want to be able to constantly retrieve it so we can feed any new RestApi
	// requests that come in and skip the overhead of asking for a new datachannel and sending a Syn
	restApiDatachannel *datachannel.DataChannel

	// fields for processing incoming kubectl commands
	localhostToken string

	// fields for opening websockets
	serviceUrl          string
	hubEndpoint         string
	params              map[string]string
	headers             map[string]string
	targetSelectHandler func(msg am.AgentMessage) (string, error)

	// fields for new datachannels
	refreshTokenCommand string
	configPath          string
	targetUser          string
	targetGroups        []string
}

func StartHTTPServer(logger *logger.Logger,
	daemonPort string,
	certPath string,
	keyPath string,
	refreshTokenCommand string,
	configPath string,
	targetUser string,
	targetGroups []string,
	localhostToken string,
	serviceUrl string,
	hubEndpoint string,
	params map[string]string,
	headers map[string]string,
	targetSelectHandler func(msg am.AgentMessage) (string, error)) error {

	listener := &HTTPServer{
		logger:              logger,
		exitMessage:         "",
		ready:               false,
		localhostToken:      localhostToken,
		serviceUrl:          serviceUrl,
		hubEndpoint:         hubEndpoint,
		params:              params,
		headers:             headers,
		targetSelectHandler: targetSelectHandler,
		configPath:          configPath,
		targetUser:          targetUser,
		targetGroups:        targetGroups,
		refreshTokenCommand: refreshTokenCommand,
	}

	// Create a new websocket
	if err := listener.newWebsocket(uuid.New().String()); err != nil {
		listener.logger.Error(err)
		return err
	}

	// Create a single datachannel for all of our rest api calls to reduce overhead
	if datachannel, err := listener.newDataChannel(string(kube.RestApi), listener.websocket); err == nil {
		listener.restApiDatachannel = datachannel
	} else {
		return err
	}

	// Create HTTP Server listens for incoming kubectl commands
	go func() {
		// Define our http handlers
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			listener.rootCallback(logger, w, r)
		})

		http.HandleFunc("/bastionzero-status", func(w http.ResponseWriter, r *http.Request) {
			listener.statusCallback(w, r)
		})

		if err := http.ListenAndServeTLS(":"+daemonPort, certPath, keyPath, nil); err != nil {
			logger.Error(err)
		}
	}()

	return nil
}

func (k *HTTPServer) statusCallback(w http.ResponseWriter, r *http.Request) {
	// Build our status message
	statusMessage := StatusMessage{
		ExitMessage: k.exitMessage,
	}

	registerJson, err := json.Marshal(statusMessage)
	if err != nil {
		k.logger.Error(fmt.Errorf("error marshalling status message: %+v", err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(registerJson)
}

// for creating new websockets
func (h *HTTPServer) newWebsocket(wsId string) error {
	subLogger := h.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, wsId, h.serviceUrl, h.hubEndpoint, h.params, h.headers, h.targetSelectHandler, autoReconnect, getChallenge, h.refreshTokenCommand); err != nil {
		return err
	} else {
		h.websocket = wsClient
		return nil
	}
}

// for creating new datachannels
func (h *HTTPServer) newDataChannel(action string, websocket *websocket.Websocket) (*datachannel.DataChannel, error) {
	// every datachannel gets a uuid to distinguish it so a single websockets can map to multiple datachannels
	dcId := uuid.New().String()
	subLogger := h.logger.GetDatachannelLogger(dcId)

	h.logger.Infof("Creating new datachannel id: %v", dcId)
	// send new datachannel message to agent
	odMessage := am.AgentMessage{
		ChannelId:   dcId,
		MessageType: string(am.OpenDataChannel),
	}
	h.websocket.Send(odMessage)

	action = "kube/" + action
	if datachannel, dcTmb, err := datachannel.New(subLogger, dcId, &h.tmb, websocket, h.refreshTokenCommand, h.configPath, action, h.targetUser, h.targetGroups); err != nil {
		h.logger.Error(err)
		return datachannel, err
	} else {

		// create a function to listen to the datachannel dying and then laugh
		go func() {
			for {
				select {
				case <-h.tmb.Dying():
					datachannel.Close(errors.New("http server closing"))
					return
				case <-dcTmb.Dying():
					// Wait until everything is dead and any close processes are sent before killing the datachannel
					dcTmb.Wait()

					// only report the error if it's not nil.  Otherwise,  we assume the datachannel closed legitimately.
					if err := dcTmb.Err(); err != nil {
						h.exitMessage = dcTmb.Err().Error()
					}

					// notify agent to close the datachannel
					h.logger.Info("Sending DataChannel Close")
					cdMessage := am.AgentMessage{
						ChannelId:   dcId,
						MessageType: string(am.CloseDataChannel),
					}
					h.websocket.Send(cdMessage)

					// close our websocket if the datachannel we closed was the last and it's not rest api
					if kube.KubeDaemonAction(action) != kube.RestApi && h.websocket.SubscriberCount() == 0 {
						h.websocket.Close(errors.New("all datachannels closed, closing websocket"))
					}
					return
				}
			}
		}()
		return datachannel, nil
	}
}

func (h *HTTPServer) bubbleUpError(w http.ResponseWriter, msg string, statusCode int) {
	w.WriteHeader(statusCode)
	h.logger.Error(errors.New(msg))
	w.Write([]byte(msg))
}

func (h *HTTPServer) rootCallback(logger *logger.Logger, w http.ResponseWriter, r *http.Request) {
	h.logger.Infof("Handling %s - %s\n", r.URL.Path, r.Method)

	// Special case /bzero-is-ready
	if strings.HasSuffix(r.URL.Path, "/bzero-is-ready") {
		if h.restApiDatachannel.Ready() {
			w.WriteHeader(http.StatusOK)
			return
		} else {
			w.WriteHeader(http.StatusTooEarly)
			return
		}
	}

	// Before processing, check if we're ready to process or if there's been an error
	switch {
	case !h.restApiDatachannel.Ready():
		h.bubbleUpError(w, "Daemon starting up...", http.StatusTooEarly)
		return
	case h.exitMessage != "":
		msg := fmt.Sprintf("error on daemon: " + h.exitMessage)
		h.bubbleUpError(w, msg, http.StatusInternalServerError)
		return
	}

	// First verify our token and extract any commands if we can
	tokenToValidate := r.Header.Get("Authorization")

	// Remove the `Bearer `
	tokenToValidate = strings.Replace(tokenToValidate, "Bearer ", "", -1)

	// Validate the token
	tokensSplit := strings.Split(tokenToValidate, securityTokenDelimiter)
	if tokensSplit[0] != h.localhostToken {
		h.bubbleUpError(w, "localhost token did not validate. Ensure you are using the right Kube config file", http.StatusInternalServerError)
		return
	}

	// Check if we have a command to extract
	command := "N/A" // TODO: should be empty string
	logId := uuid.New().String()
	if len(tokensSplit) == 3 {
		command = tokensSplit[1]
		logId = tokensSplit[2]
	}

	// parse action from incoming request
	switch {
	// interactive commands that require both stdin and stdout
	case isExecRequest(r):
		// create new datachannel and feed it kubectl handlers
		if datachannel, err := h.newDataChannel(string(kube.Exec), h.websocket); err == nil {
			datachannel.Feed(string(kube.Exec), logId, command, w, r)
		}
	// Similar to exec it's interactive but instead has a input and error stream
	case isPortForwardRequest(r):
		// create new datachannel and feed it kubectl handlers
		if datachannel, err := h.newDataChannel(string(kube.Stream), h.websocket); err == nil {
			datachannel.Feed(string(kube.PortForward), logId, command, w, r)
		}
	// persistent, yet not interactive commands that serve continual output but only listen for a single, request-cancelling input
	case isStreamRequest(r):
		// create new datachannel and feed it kubectl handlers
		if datachannel, err := h.newDataChannel(string(kube.Stream), h.websocket); err == nil {
			datachannel.Feed(string(kube.Stream), logId, command, w, r)
		}

	// simple call and response aka restapi requests
	default:
		// grab our existing rest api datachannel and feed it new handlers
		h.restApiDatachannel.Feed(string(kube.RestApi), logId, command, w, r)
	}
}

func isPortForwardRequest(request *http.Request) bool {
	return strings.HasSuffix(request.URL.Path, "/portforward")
}

func isExecRequest(request *http.Request) bool {
	return strings.HasSuffix(request.URL.Path, "/exec") || strings.HasSuffix(request.URL.Path, "/attach")
}

func isStreamRequest(request *http.Request) bool {
	return (strings.HasSuffix(request.URL.Path, "/log") && kubeutils.IsQueryParamPresent(request, "follow")) || kubeutils.IsQueryParamPresent(request, "watch")
}
