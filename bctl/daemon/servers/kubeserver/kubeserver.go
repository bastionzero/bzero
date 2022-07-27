package kubeserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
	bzkube "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
)

const (
	// This token is used when validating our Bearer token. Our token comes in with the form "{localhostToken}++++{english command i.e. zli kube get pods}++++{logId}"
	// The english command and logId are only generated if the user is using "zli kube ..."
	// So we use this securityTokenDelimiter to split up our token and extract what might be there
	securityTokenDelimiter = "++++"

	// websocket connection parameters
	autoReconnect = true
)

type StatusMessage struct {
	ExitMessage string `json:"ExitMessage"`
}

type KubeServer struct {
	logger      *logger.Logger
	connection  *websocket.Websocket
	tmb         tomb.Tomb
	exitMessage string

	// fields for processing incoming kubectl commands
	localhostToken string

	// fields for new datachannels
	cert         *bzcert.BZCert
	targetUser   string
	targetGroups []string
	agentPubKey  string
}

func StartKubeServer(
	logger *logger.Logger,
	localPort string,
	localHost string,
	certPath string,
	keyPath string,
	cert *bzcert.BZCert,
	targetUser string,
	targetGroups []string,
	localhostToken string,
	serviceUrl string,
	connUrl string,
	params map[string][]string,
	headers map[string][]string,
	agentPubKey string,
) error {

	server := &KubeServer{
		logger:         logger,
		exitMessage:    "",
		localhostToken: localhostToken,
		cert:           cert,
		targetGroups:   targetGroups,
		agentPubKey:    agentPubKey,
	}

	// Create our one connection in the form of a websocket
	subLogger := logger.GetWebsocketLogger(uuid.New().String())
	if client, err := websocket.New(subLogger, serviceUrl, connUrl, params, headers, autoReconnect, websocket.DaemonWebsocket); err != nil {
		return err
	} else {
		server.connection = client
	}

	// Create HTTP Server listens for incoming kubectl commands
	go func() {
		// Define our http handlers
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			server.rootCallback(logger, w, r)
		})

		http.HandleFunc("/bastionzero-ready", func(w http.ResponseWriter, r *http.Request) {
			server.isReadyCallback(w, r)
		})

		http.HandleFunc("/bastionzero-status", func(w http.ResponseWriter, r *http.Request) {
			server.statusCallback(w, r)
		})

		if err := http.ListenAndServeTLS(localHost+":"+localPort, certPath, keyPath, nil); err != nil {
			logger.Error(err)
		}
	}()

	return nil
}

// TODO: this logic may no longer be necessary, but would require a zli change to remove
func (k *KubeServer) isReadyCallback(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (k *KubeServer) statusCallback(w http.ResponseWriter, r *http.Request) {
	// Build our status message
	statusMessage := StatusMessage{
		ExitMessage: k.exitMessage,
	}

	if registerJson, err := json.Marshal(statusMessage); err != nil {
		k.logger.Errorf("error marshalling status message: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		w.WriteHeader(http.StatusOK)
		w.Write(registerJson)
	}
}

// for creating new datachannels
func (k *KubeServer) newDataChannel(dcId string, action string, connection *websocket.Websocket, plugin *kube.KubeDaemonPlugin, writer http.ResponseWriter) error {
	subLogger := k.logger.GetDatachannelLogger(dcId)

	k.logger.Infof("Creating new datachannel id: %s", dcId)

	// Build the actionParams to send to the datachannel to start the plugin
	synPayload := bzkube.KubeActionParams{
		TargetUser:   k.targetUser,
		TargetGroups: k.targetGroups,
	}

	ksLogger := k.logger.GetComponentLogger("mrzap")
	keysplitter, err := keysplitting.New(ksLogger, k.agentPubKey, k.cert)
	if err != nil {
		return err
	}

	action = "kube/" + action
	attach := false
	dc, dcTmb, err := datachannel.New(subLogger, dcId, &k.tmb, connection, keysplitter, plugin, action, synPayload, attach, true)
	if err != nil {
		return err
	}

	// create a function to listen to the datachannel dying and then laugh
	go func() {
		for {
			select {
			case <-k.tmb.Dying():
				dc.Close(errors.New("kube server closing"))
				return
			case <-dcTmb.Dead():
				// only report the error if it's not nil.  Otherwise,  we assume the datachannel closed legitimately.
				if err := dcTmb.Err(); err != nil {
					errs := strings.Split(dcTmb.Err().Error(), ": ")
					msg := fmt.Sprintf("error: %s", errs[len(errs)-1])
					k.bubbleUpError(writer, msg, 500)
				}

				// notify agent to close the datachannel
				k.logger.Info("Sending DataChannel Close")
				cdMessage := am.AgentMessage{
					ChannelId:   dcId,
					MessageType: string(am.CloseDataChannel),
				}
				k.connection.Send(cdMessage)
				return
			}
		}
	}()
	return nil
}

func (k *KubeServer) bubbleUpError(w http.ResponseWriter, msg string, statusCode int) {
	w.WriteHeader(statusCode)
	k.logger.Error(errors.New(msg))
	w.Write([]byte(msg))
}

func (k *KubeServer) rootCallback(logger *logger.Logger, w http.ResponseWriter, r *http.Request) {
	k.logger.Infof("Handling %s - %s\n", r.URL.Path, r.Method)

	// First verify our token and extract any commands if we can
	tokenToValidate := r.Header.Get("Authorization")

	// Remove the `Bearer `
	tokenToValidate = strings.Replace(tokenToValidate, "Bearer ", "", -1)

	// Validate the token
	tokensSplit := strings.Split(tokenToValidate, securityTokenDelimiter)
	if tokensSplit[0] != k.localhostToken {
		k.bubbleUpError(w, "localhost token did not validate. Ensure you are using the right Kube config file", http.StatusInternalServerError)
		return
	}

	// Check if we have a command to extract
	command := "N/A" // TODO: should be empty string
	logId := uuid.New().String()
	if len(tokensSplit) == 3 {
		command = tokensSplit[1]
		logId = tokensSplit[2]
	}

	// Determine the action
	action := getAction(r)

	// start up our plugin
	// every datachannel gets a uuid to distinguish it so a single websockets can map to multiple datachannels
	dcId := uuid.New().String()

	pluginLogger := logger.GetPluginLogger(bzplugin.Kube)
	pluginLogger = pluginLogger.GetDatachannelLogger(dcId)
	plugin := kube.New(pluginLogger, k.targetUser, k.targetGroups)

	if err := k.newDataChannel(dcId, string(action), k.connection, plugin, w); err != nil {
		k.logger.Error(err)
	}

	if err := plugin.StartAction(action, logId, command, w, r); err != nil {
		logger.Errorf("error starting action: %s", err)
	}
}

func getAction(req *http.Request) bzkube.KubeAction {
	// parse action from incoming request
	switch {
	// interactive commands that require both stdin and stdout
	case isExecRequest(req):
		return bzkube.Exec

	// Persistent, yet not interactive commands that serve continual output but only listen for a single, request-cancelling input
	case isPortForwardRequest(req):
		return bzkube.PortForward
	case isStreamRequest(req):
		return bzkube.Stream

	// simple call and response aka restapi requests
	default:
		return bzkube.RestApi
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
