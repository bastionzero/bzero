package websocket

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/agent/vault"
	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/controllers/agentcontroller"
	"bastionzero.com/bctl/v1/bzerolib/controllers/connectionnodecontroller"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/mrzap/util"
)

const (
	// SignalR Constants
	signalRMessageTerminatorByte = 0x1E
	signalRTypeNumber            = 1 // Ref: https://github.com/aspnet/SignalR/blob/master/specs/HubProtocol.md#invocation-message-encoding

	// Enum target types
	Cluster = 2
	Db      = 3
	Web     = 4

	// Enum target types for agent side connections
	AgentWebsocket      = -1
	ClusterAgentControl = -2

	// Hub endpoints
	kubeDaemonConnectionNodeHubEndpoint = "/hub/daemon"
	kubeAgentConnectionNodeHubEndpoint  = "/hub/agent"

	controlHubEndpoint = "/api/v1/hub/kube-control"
)

type IWebsocket interface {
	Connect() error
	Send(agentMessage am.AgentMessage)
}

// This will be the client that we use to store our websocket connection
type Websocket struct {
	client     *websocket.Conn
	logger     *logger.Logger
	ready      bool
	subscribed bool
	tmb        tomb.Tomb
	channels   map[string]IChannel

	// Ref: https://github.com/gorilla/websocket/issues/119#issuecomment-198710015
	socketLock sync.Mutex
	mapLock    sync.RWMutex

	// Buffered channel to keep track of outgoing messages
	sendQueue chan am.AgentMessage

	// Function for figuring out correct Target SignalR Hub
	targetSelectHandler func(msg am.AgentMessage) (string, error)

	// Flag to indicate if we should automatically try to reconnect
	autoReconnect bool

	// Flag for grabbing a challenge (required of the agent control channel to connect to bastion)
	getChallenge bool

	// Variables used to make the connection
	serviceUrl    string
	params        map[string]string // Params used to authenticate against bastion
	requestParams map[string]string // Params used to authenticate against the websocket
	headers       map[string]string

	// Optional command to refresh auth information
	refreshTokenCommand string

	// Target type for connectionNode
	targetType int

	// Base url to make request
	baseUrl string
}

// Constructor to create a new common websocket client object that can be shared by the daemon and server
func New(logger *logger.Logger,
	id string,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	targetSelectHandler func(msg am.AgentMessage) (string, error),
	autoReconnect bool,
	getChallenge bool,
	refreshTokenCommand string,
	targetType int) (*Websocket, error) {

	ws := Websocket{
		logger:              logger,
		sendQueue:           make(chan am.AgentMessage, 100),
		channels:            make(map[string]IChannel),
		targetSelectHandler: targetSelectHandler,
		getChallenge:        getChallenge,
		autoReconnect:       autoReconnect,
		serviceUrl:          serviceUrl,
		params:              params,
		requestParams:       make(map[string]string),
		headers:             headers,
		subscribed:          false,
		refreshTokenCommand: refreshTokenCommand,
		targetType:          targetType,
		baseUrl:             "",
	}

	// Connect to the websocket in a go routine incase it takes a long time
	go ws.Connect()

	// Listener for any incoming messages
	ws.tmb.Go(func() error {
		for ws.tmb.Alive() {
			for ws.ready { // Might want a sync.Cond in the future if we're doing optimization
				select {
				case <-ws.tmb.Dying():
					return nil
				default:
					if err := ws.receive(); err != nil {
						ws.logger.Error(err)
					}
				}
			}
		}
		return nil
	})

	// Listener for any messages that need to be sent
	go func() {
		for ws.tmb.Alive() {
			for ws.ready { // Might want a sync.Cond in the future if we're doing optimization
				select {
				case <-ws.tmb.Dying():
					return
				case msg := <-ws.sendQueue:
					ws.processOutput(msg)
				}
			}
		}
	}()

	return &ws, nil
}

func (w *Websocket) Close(reason error) {
	// close all of our existing datachannels
	for _, channel := range w.channels {
		channel.Close(reason)
	}

	// tell our tmb to clean up after us
	w.logger.Infof("websocket closed because: %s", reason)
	w.tmb.Kill(reason)
	w.tmb.Wait()
}

// add channel to channels dictionary for forwarding incoming messages
func (w *Websocket) Subscribe(id string, channel IChannel) {
	w.addChannel(id, channel)
}

// remove channel from channel dictionary
func (w *Websocket) Unsubscribe(id string) {
	w.deleteChannel(id)
}

func (w *Websocket) SubscriberCount() int {
	return w.lenChannels()
}

// Returns error on websocket closed
func (w *Websocket) receive() error {

	// Read incoming message(s)
	_, rawMessage, err := w.client.ReadMessage()

	if err != nil {
		w.ready = false

		// Check if it's a clean exit or we don't need to reconnect
		if websocket.IsCloseError(err, websocket.CloseNormalClosure) || !w.autoReconnect {
			rerr := errors.New("websocket closed")
			w.Close(rerr)
			return rerr
		} else { // else, reconnect
			msg := fmt.Errorf("error in websocket, will attempt to reconnect: %s", err)
			w.logger.Error(msg)
			w.Connect()
		}
	} else {
		if messages, err := w.unwrapSignalR(rawMessage); err != nil {
			return err
		} else {
			for _, message := range messages {
				switch message.Target {
				case "CloseConnection":
					rerr := errors.New("closing message received; websocket closed")
					w.Close(rerr)
					return rerr
				case "ReadyBastionToClient": // Bastion letting client know the websocket's ready
					w.logger.Info("Connection Ready")
					w.ready = true
				default:
					w.ready = true

					// push to the right channel
					agentMessage := message.Arguments[0]

					if channel, ok := w.getChannel(agentMessage.ChannelId); ok {
						channel.Receive(agentMessage)
					} else {
						err := fmt.Errorf("received message that did not correspond to existing channel: %s", agentMessage.ChannelId)
						w.logger.Error(err)
					}
				}
			}
		}
	}
	return nil
}

func (w *Websocket) unwrapSignalR(rawMessage []byte) ([]SignalRWrapper, error) {
	// Always trim off the termination char if its there
	if rawMessage[len(rawMessage)-1] == signalRMessageTerminatorByte {
		rawMessage = rawMessage[0 : len(rawMessage)-1]
	}

	// Also check to see if we have multiple messages
	splitmessages := bytes.Split(rawMessage, []byte{signalRMessageTerminatorByte})

	messages := []SignalRWrapper{}
	for _, msg := range splitmessages {
		// unwrap signalR
		var wrappedMessage SignalRWrapper
		if err := json.Unmarshal(msg, &wrappedMessage); err != nil {
			return messages, fmt.Errorf("error unmarshalling SignalR message from Bastion: %v", string(msg))
		}

		// if the messages isn't the signalr type we're expecting, ignore it because it's not going to be an AgentMessage
		if wrappedMessage.Type != signalRTypeNumber {
			msg := fmt.Sprintf("Ignoring SignalR message with type %v", wrappedMessage.Type)
			w.logger.Trace(msg)
		} else if len(wrappedMessage.Arguments) != 0 { // make sure there is an AgentMessage
			messages = append(messages, wrappedMessage)
		}
	}
	return messages, nil
}

// Function to write signalr message to websocket
func (w *Websocket) processOutput(agentMessage am.AgentMessage) {
	// Lock our send function so we don't hit any concurrency issues
	// Ref: https://github.com/gorilla/websocket/issues/698
	w.socketLock.Lock()
	defer w.socketLock.Unlock()

	// Select SignalR Endpoint
	target, err := w.targetSelectHandler(agentMessage) // Agent and Daemon specify their own function to choose target
	if err != nil {
		rerr := fmt.Errorf("error in selecting SignalR Endpoint target name: %s", err)
		w.logger.Error(rerr)
		return
	}

	signalRMessage := SignalRWrapper{
		Target:    target,
		Type:      signalRTypeNumber,
		Arguments: []am.AgentMessage{agentMessage},
	}

	// Write our message to websocket
	if msgBytes, err := json.Marshal(signalRMessage); err != nil {
		w.logger.Error(fmt.Errorf("error marshalling outgoing SignalR Message: %v", signalRMessage))
	} else {
		if err := w.client.WriteMessage(websocket.TextMessage, append(msgBytes, signalRMessageTerminatorByte)); err != nil {
			w.logger.Error(err)
		}
	}
}

// Function to write signalr message to websocket
func (w *Websocket) Send(agentMessage am.AgentMessage) {
	w.sendQueue <- agentMessage
}

func (w *Websocket) Connect() {
	if w.getChallenge {
		// First get the config from the vault
		config, _ := vault.LoadVault()

		// Build our agentController
		agentController := agentcontroller.New(w.logger, w.serviceUrl, map[string]string{}, map[string]string{}, w.params["target_type"])

		// If we have a private key, we must solve the challenge
		if solvedChallenge, err := agentController.GetChallenge(w.params["org_id"], w.params["target_id"], config.Data.TargetName, config.Data.PrivateKey, w.params["target_type"]); err != nil {
			w.logger.Error(fmt.Errorf("error getting challenge: %s", err))
			return
		} else {
			w.params["solved_challenge"] = solvedChallenge
		}

		// And sign our agent version
		if signedAgentVersion, err := agentcontroller.SignString(config.Data.PrivateKey, w.params["agent_version"]); err != nil {
			w.logger.Error(fmt.Errorf("error signing agent version: %s", err))
			return
		} else {
			w.params["signed_agent_version"] = signedAgentVersion
		}
	}

	// If we have the option to refresh our auth details do it here before reconnecting
	if w.refreshTokenCommand != "" {
		if err := util.RunRefreshAuthCommand(w.refreshTokenCommand); err != nil {
			w.logger.Errorf("error executing refresh auth command: %v", err)
			panic(err)
		}
	}

	// Switch based on the targetType
	switch w.targetType {
	case Cluster:
		// Define our bastionURL
		bastionUrl := "https://" + w.serviceUrl

		// First hit Bastion in order to get the connectionNode information, build our controller
		cnControllerLogger := w.logger.GetComponentLogger("cncontroller")
		cnController, cnControllerErr := connectionnodecontroller.New(cnControllerLogger, bastionUrl, "", w.headers, w.params)
		if cnControllerErr != nil {
			w.logger.Error(fmt.Errorf("error creating cnController"))
			return
		}

		targetGroups := []string{}
		if w.params["target_groups"] != "" {
			targetGroups = strings.Split(w.params["target_groups"], ",")
		}

		createConnectionResponse := cnController.CreateKubeConnection(w.params["target_user"], targetGroups, w.params["target_id"])

		// Now we can build our connectionnode url
		w.baseUrl = w.buildConnectionNodeUrl(createConnectionResponse.ConnectionNodeId) + kubeDaemonConnectionNodeHubEndpoint

		// Define our request params
		w.requestParams["connectionId"] = createConnectionResponse.ConnectionId
		w.requestParams["authToken"] = createConnectionResponse.AuthToken

		w.requestParams["websocketType"] = w.params["websocketType"]
	case Db:
		// Define our bastionURL
		bastionUrl := "https://" + w.serviceUrl

		// First hit Bastion in order to get the connectionNode information, build our controller
		cnControllerLogger := w.logger.GetComponentLogger("cncontroller")
		cnController, cnControllerErr := connectionnodecontroller.New(cnControllerLogger, bastionUrl, "", w.headers, w.params)
		if cnControllerErr != nil {
			w.logger.Error(fmt.Errorf("error creating cnController"))
			return
		}

		createConnectionResponse := cnController.CreateDbConnection(w.params["target_id"])

		// Now we can build our connectionnode url
		w.baseUrl = w.buildConnectionNodeUrl(createConnectionResponse.ConnectionNodeId) + kubeDaemonConnectionNodeHubEndpoint

		// Define our request params
		w.requestParams["connectionId"] = createConnectionResponse.ConnectionId
		w.requestParams["authToken"] = createConnectionResponse.AuthToken

		w.requestParams["websocketType"] = w.params["websocketType"]
	case Web:
		// Define our bastionURL
		bastionUrl := "https://" + w.serviceUrl

		// First hit Bastion in order to get the connectionNode information, build our controller
		cnControllerLogger := w.logger.GetComponentLogger("cncontroller")
		cnController, cnControllerErr := connectionnodecontroller.New(cnControllerLogger, bastionUrl, "", w.headers, w.params)
		if cnControllerErr != nil {
			w.logger.Error(fmt.Errorf("error creating cnController"))
			return
		}

		createConnectionResponse := cnController.CreateWebConnection(w.params["target_id"])

		// Now we can build our connectionnode url
		w.baseUrl = w.buildConnectionNodeUrl(createConnectionResponse.ConnectionNodeId) + kubeDaemonConnectionNodeHubEndpoint

		// Define our request params
		w.requestParams["connectionId"] = createConnectionResponse.ConnectionId
		w.requestParams["authToken"] = createConnectionResponse.AuthToken

		w.requestParams["websocketType"] = w.params["websocketType"]
	case AgentWebsocket:
		// Build our connectionnode Url
		w.baseUrl = w.buildConnectionNodeUrl(w.params["connection_node_id"]) + kubeAgentConnectionNodeHubEndpoint

		// Define our reqest params
		w.requestParams["daemon_websocket_id"] = w.params["daemon_websocket_id"]
		w.requestParams["token"] = w.params["token"]
		w.requestParams["connection_type"] = w.params["connection_type"]
	case ClusterAgentControl:
		// Default base url is just the service url and the hub endpoint
		// This is because we hit bastion to initiate our control hub
		w.baseUrl = w.serviceUrl + controlHubEndpoint

		// Default request params are just the params based
		w.requestParams = w.params
	default:
		w.logger.Error(fmt.Errorf("unhandled target type; %d", w.targetType))
		return
	}

	// Make our POST request
	negotiateEndpoint := "https://" + w.baseUrl + "/negotiate"
	w.logger.Infof("Starting negotiation with endpoint %s", negotiateEndpoint)

	if response, err := bzhttp.Post(w.logger, negotiateEndpoint, "application/json", []byte{}, w.headers, w.requestParams); err != nil {
		w.logger.Error(fmt.Errorf("error on negotiation: %s. Response: %+v", err, response))
		return
	} else {

		// Extract out the connection token
		bodyBytes, _ := ioutil.ReadAll(response.Body)
		var m map[string]interface{}

		if err := json.Unmarshal(bodyBytes, &m); err != nil {
			// TODO: Add error handling around this, we should at least retry and then bubble up the error to the user
			w.logger.Error(fmt.Errorf("error un-marshalling negotiate response: %+v", m))
		}

		// Add the connection id to the list of params
		w.params["id"] = m["connectionId"].(string)
		w.params["clientProtocol"] = "1.5"
		w.params["transport"] = "WebSockets"

		w.logger.Info("Negotiation successful")
		response.Body.Close()
	}

	// Build our url u , add our params as well
	// websocketUrl := url.URL{Scheme: "wss", Host: w.serviceUrl, Path: w.hubEndpoint}
	websocketRawUrl := "wss://" + w.baseUrl
	websocketUrl, urlParseError := url.Parse(websocketRawUrl)
	if urlParseError != nil {
		w.logger.Error(fmt.Errorf("error parsing url %s", websocketRawUrl))
		return
	}
	w.logger.Infof("Connecting to %s", websocketUrl.String())

	q := websocketUrl.Query()
	for key, value := range w.requestParams {
		q.Set(key, value)
	}
	websocketUrl.RawQuery = q.Encode()

	var err error
	if w.client, _, err = websocket.DefaultDialer.Dial(websocketUrl.String(), http.Header{}); err != nil {
		w.logger.Error(err)
		return
	} else {
		// Define our protocol and version
		// Ref: https://stackoverflow.com/questions/65214787/signalr-websockets-and-go
		if err := w.client.WriteMessage(websocket.TextMessage, append([]byte(`{"protocol": "json","version": 1}`), signalRMessageTerminatorByte)); err != nil {
			w.logger.Info("Error when trying to agree on version for SignalR!")
			w.client.Close()
		} else {
			w.logger.Info("Connection successful!")
			w.ready = true
		}
	}
}

func (w *Websocket) lenChannels() int {
	w.mapLock.Lock()
	defer w.mapLock.Unlock()

	return len(w.channels)
}

func (w *Websocket) addChannel(id string, channel IChannel) {
	// Helper function so we avoid writing to this map at the same time
	w.mapLock.Lock()
	defer w.mapLock.Unlock()

	w.channels[id] = channel
}

func (w *Websocket) deleteChannel(id string) {
	w.mapLock.Lock()
	defer w.mapLock.Unlock()

	delete(w.channels, id)
}

func (w *Websocket) getChannel(id string) (IChannel, bool) {
	w.mapLock.Lock()
	defer w.mapLock.Unlock()

	channel, ok := w.channels[id]
	return channel, ok
}

// Helper function to build connection node Url from our base url
func (w *Websocket) buildConnectionNodeUrl(connectionNodeId string) string {
	// Determine the prefix of the url (i.e. cloud.bastionzero.com -> cloud)
	urlPrefix := strings.Split(w.serviceUrl, ".bastionzero.com")[0]

	// Build the connect url
	return urlPrefix + "-connect.bastionzero.com/" + connectionNodeId
}
