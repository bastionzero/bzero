package websocket

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"

	backoff "github.com/cenkalti/backoff/v4"
	"github.com/gorilla/websocket"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/agent/vault"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket/challenge"
	"bastionzero.com/bctl/v1/bzerolib/connection/signalr"
	newws "bastionzero.com/bctl/v1/bzerolib/connection/websocket"
	"bastionzero.com/bctl/v1/bzerolib/controllers/connectionnodecontroller"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

// Connection Type enum
type ConnectionType string

const (
	SHELL  ConnectionType = "SHELL"
	TUNNEL ConnectionType = "TUNNEL"
	FUD    ConnectionType = "FUD"
	KUBE   ConnectionType = "CLUSTER"
	DB     ConnectionType = "DB"
	WEB    ConnectionType = "WEB"
)

type WebsocketType int

const (
	// Enum target types for agent side connections
	AgentWebsocket  WebsocketType = -1
	AgentControl    WebsocketType = -2
	DaemonWebsocket WebsocketType = -3
)

const (
	// Hub endpoints
	daemonHubEndpoint = "hub/daemon"
	agentHubEndpoint  = "hub/agent"

	controlHubEndpoint = "/api/v1/hub/control"

	AgentConnectedWebsocketTimeout = 30 * time.Second
)

type IWebsocket interface {
	Send(agentMessage am.AgentMessage)
	Unsubscribe(id string)
	Subscribe(id string, channel IChannel)
	Close(error)
	Ready() bool
}

// https://github.com/aspnet/SignalR/blob/master/specs/HubProtocol.md#invocations
type signalRInvocationMessage struct {
	invocationId *string
	agentMessage am.AgentMessage
}

// This will be the client that we use to store our websocket connection
type Websocket struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	client     *websocket.Conn
	ready      bool
	subscribed bool
	channels   map[string]IChannel

	// Ref: https://github.com/gorilla/websocket/issues/119#issuecomment-198710015
	socketLock sync.Mutex
	mapLock    sync.RWMutex

	// InvocationId
	currentInvocationId int64
	invocationIdLock    sync.Mutex

	// Buffered channel to keep track of outgoing messages
	sendQueue               chan signalRInvocationMessage
	messagesWaitingResponse map[string]am.AgentMessage

	// Flag to indicate if we should automatically try to reconnect
	autoReconnect bool

	// Flag for grabbing a challenge (required of the agent control channel to connect to bastion)
	getChallenge bool

	// Variables used to make the connection
	connectionUrl *url.URL
	params        map[string][]string
	headers       map[string][]string

	// Type of connection being made over the websocket
	myType WebsocketType

	// Agent Ready Channel indicates when the agent has connected to the
	// corresponding websocket. This is only used for daemon websocket.
	agentReadyChan chan struct{}

	// True when websocket is ready to start sending output messages
	sendQueueReady bool
}

// Constructor to create a new common websocket client object that can be shared by the daemon and server
func New(
	logger *logger.Logger,
	bastionUrl string,
	connectionUrl string,
	params map[string][]string,
	headers map[string][]string,
	autoReconnect bool,
	wtype WebsocketType,
) (*Websocket, error) {

	// Check if the serviceUrl is a valid url
	u, err := url.Parse(connectionUrl)
	if err != nil {
		return nil, err
	}

	ws := Websocket{
		logger:                  logger,
		sendQueue:               make(chan signalRInvocationMessage, 100),
		messagesWaitingResponse: make(map[string]am.AgentMessage),
		channels:                make(map[string]IChannel),
		autoReconnect:           autoReconnect,
		connectionUrl:           u,
		params:                  params,
		headers:                 headers,
		myType:                  wtype,
		agentReadyChan:          make(chan struct{}, 1),
	}

	// Connect to the websocket in a go routine in case it takes a long time
	go func() {
		if err := ws.connect(); err != nil {
			logger.Error(err)
			go ws.Close(fmt.Errorf("process was unable to connect to BastionZero"))

			// If this is a daemon connection (i.e. we are not getting a challenge)
			// we also need to make sure we close the connection in the backend
			// LUCIE: more reliable to call connectionType because it's named the same in both
			if ws.requestParams["connectionId"] != "" {
				cnControllerLogger := ws.logger.GetComponentLogger("cncontroller")
				cnController, cnControllerErr := connectionnodecontroller.New(cnControllerLogger, bastionUrl, "", ws.headers, ws.params)
				if cnControllerErr != nil {
					ws.logger.Errorf("error building connection controller to close connection %s", cnControllerErr)
				}
				cnController.CloseConnection(ws.requestParams["connectionId"])
				logger.Infof("Closed connection: %s", ws.requestParams["connectionId"])
			}
		}
	}()

	ws.tmb.Go(func() error {
		// Listener for any messages that need to be sent
		ws.tmb.Go(func() error {
			for ws.tmb.Alive() {
				for ws.ready { // Might want a sync.Cond in the future if we're doing optimization
					select {
					case <-ws.tmb.Dying():
						return nil
					case msg := <-ws.sendQueue:
						ws.waitForAgentWebsocketReady()
						ws.processOutput(msg)
					}
				}
				time.Sleep(time.Second)
			}
			return nil
		})

		// Receive any messages in the websocket
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

	return &ws, nil
}

func (w *Websocket) Close(reason error) {
	w.logger.Infof("websocket closing because: %s", reason)

	// close all of our existing datachannels
	for _, channel := range w.channels {
		channel.Close(reason)
	}

	// mark the tmb as dying so we ignore any errors that occur when closing the
	// websocket
	w.tmb.Kill(reason)

	// close the websocket connection. This will cause errors when reading from
	// websocket in receive
	if w.client != nil {
		w.ready = false
		w.client.Close()
	}

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

// Returns error on websocket closed
func (w *Websocket) receive() error {
	// Read incoming message(s)
	_, rawMessage, err := w.client.ReadMessage()

	// We check to make sure the tmb is still alive and not being actively
	// killed because otherwise an error in receive will call w.Close which will
	// attempt to close/wait on the tmb again and cause a deadlock
	if err != nil && !w.tmb.Alive() {
		// We are already killing the tmb so just return
		return nil
	} else if err != nil {
		w.ready = false

		// Check if it's a clean exit or we don't need to reconnect
		if websocket.IsCloseError(err, websocket.CloseNormalClosure) || !w.autoReconnect {
			rerr := errors.New("websocket closed")
			w.Close(rerr)
			return rerr
		} else { // else, reconnect
			msg := fmt.Errorf("error in websocket, will attempt to reconnect: %s", err)
			w.logger.Error(msg)
			if err := w.connect(); err != nil {
				return err
			}
		}
	} else {
		if messages, err := w.unwrapSignalR(rawMessage); err != nil {
			return err
		} else {
			for _, message := range messages {
				switch message.Target {
				case DaemonCloseConnection:
					rerr := errors.New("the bzero agent terminated the connection")
					w.Close(rerr)
					return rerr
				case AgentConnected:
					// Signal the agentReady channel when we receive a message
					// from the connection node that the agent websocket is
					// connected
					var agentConnectedMessage AgentConnectedMessage
					if err := json.Unmarshal(message.Arguments[0], &agentConnectedMessage); err != nil {
						return fmt.Errorf("error unmarshalling agent connected message. Error: %s", err)
					}

					w.logger.Infof("Agent is connected and ready to receive methods in websocket for connection: %s", agentConnectedMessage.ConnectionId)

					w.agentReadyChan <- struct{}{}
				default:
					w.ready = true

					// Otherwise assume that the invocation contains a single AgentMessage argument
					if len(message.Arguments) != 1 {
						return fmt.Errorf("expected a single agent message argument but got %d arguments.", len(message.Arguments))
					}

					var agentMessage am.AgentMessage
					if err := json.Unmarshal(message.Arguments[0], &agentMessage); err != nil {
						return fmt.Errorf("error unmarshalling agent message from websocket method %s. Error: %s", message.Target, err)
					}

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

func (w *Websocket) unwrapSignalR(rawMessage []byte) ([]SignalRInvocationMessage, error) {
	// Always trim off the termination char if its there
	if rawMessage[len(rawMessage)-1] == signalRMessageTerminatorByte {
		rawMessage = rawMessage[0 : len(rawMessage)-1]
	}

	// Also check to see if we have multiple messages
	splitmessages := bytes.Split(rawMessage, []byte{signalRMessageTerminatorByte})

	messages := []SignalRInvocationMessage{}
	for _, msg := range splitmessages {

		// unwrap signalR
		var signalRMessageType SignalRMessageTypeOnly
		if err := json.Unmarshal(msg, &signalRMessageType); err != nil {
			return messages, fmt.Errorf("error unmarshalling SignalR message from Bastion: %v", string(msg))
		}

		switch SignalRMessageType(signalRMessageType.Type) {
		case Completion:
			var completionMessage SignalRCompletionMessage
			if err := json.Unmarshal(msg, &completionMessage); err != nil {
				return messages, fmt.Errorf("error unmarshalling SignalR completion message from Bastion: %v", string(msg))
			}

			if completionMessage.InvocationId != nil {
				invocationId := *completionMessage.InvocationId
				message := w.getAgentMessageFromInvocationId(invocationId)

				if completionMessage.Error != nil {
					w.logger.Errorf("Error invoking Agent message type %s on channel %s. Unhandled Server Error: %s", message.MessageType, message.ChannelId, *completionMessage.Error)
				}
				if completionMessage.Result != nil {
					if completionMessage.Result.Error {
						w.logger.Errorf("Error invoking Agent message type %s for channel %s: %s", message.MessageType, message.ChannelId, *completionMessage.Result.ErrorMessage)
					} else {
						w.logger.Tracef("Successfully completed invocation for Agent message type %s", message.MessageType)
					}
				}

				w.deleteInvocationId(invocationId)
			} else {
				return messages, fmt.Errorf("error received completion message from Bastion without a invocationId: %v", string(msg))
			}
		case Invocation:
			var invocationMessage SignalRInvocationMessage
			if err := json.Unmarshal(msg, &invocationMessage); err != nil {
				return messages, fmt.Errorf("error unmarshalling SignalR invocation message from Bastion: %s. Error: %s", string(msg), err)
			}

			w.logger.Tracef("Received new Invocation Message with target: %s", invocationMessage.Target)
			messages = append(messages, invocationMessage)
		default:
			msg := fmt.Sprintf("Ignoring SignalR message with type %v", signalRMessageType.Type)
			w.logger.Tracef(msg)
		}
	}
	return messages, nil
}

// Function to write signalr message to websocket
func (w *Websocket) processOutput(message signalRInvocationMessage) {
	// Lock our send function so we don't hit any concurrency issues
	// Ref: https://github.com/gorilla/websocket/issues/698
	w.socketLock.Lock()
	defer w.socketLock.Unlock()

	// Select SignalR Endpoint
	target, err := w.websocketMethodSelector(message.agentMessage)
	if err != nil {
		rerr := fmt.Errorf("error in selecting SignalR Endpoint target name: %s", err)
		w.logger.Error(rerr)
		return
	}

	agentMessageArg, err := json.Marshal(message.agentMessage)
	if err != nil {
		w.logger.Errorf("Failed to marshal agent message in signalR invocation message: %s", err)
		return
	}

	signalRMessage := SignalRInvocationMessage{
		Target:       target,
		Type:         int(Invocation),
		Arguments:    []json.RawMessage{agentMessageArg},
		InvocationId: message.invocationId,
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

	// TODO: we can make invocationId optional by making it nil which will
	// indicate to the server this is a non-blocking invocation. This function
	// should take a boolean to indicate if it should wait for a response. TBD
	// how should we return the result of the invocation to the caller context.
	// Maybe by creating and pushing to a new channel specific to this
	// invocationId?
	invocationId := w.createInvocationId(agentMessage)

	w.sendQueue <- signalRInvocationMessage{
		agentMessage: agentMessage,
		invocationId: &invocationId,
	}
}

// Opens a websocket connection to Bastion
//
// in order for Connect to serve as a robust abstraction for other processes that rely on it,
// it must handle its own retry logic. For this, we use an exponential backoff. Some failures
// within the connection process are considered transient, and thus trigger a retry. Others are
// considered fatal, and return an error
//
// NOTE: with the exception of negotiate(), underlying bzhttp requests have their own 8-hour
// exponential backoff, and so their errors are considered fatal
func (w *Websocket) connect() error {
	// determine our target endpoint
	// Switch based on the targetType
	var endpoint string
	var targetSelectHandler func(msg am.AgentMessage) (string, error)
	switch w.myType {
	case DaemonWebsocket:
		endpoint = daemonHubEndpoint
		targetSelectHandler = daemonTargetSelector
	case AgentWebsocket:
		endpoint = agentHubEndpoint
		targetSelectHandler = agentControlChannelTargetSelector
	case AgentControl:
		endpoint = controlHubEndpoint
		targetSelectHandler = agentDataChannelTargetSelector
	default:
		return fmt.Errorf("unhandled connection type: %v", w.myType)
	}

	// Create our signalr object
	srLogger := w.logger.GetComponentLogger("SignalR")
	conn := signalr.New(srLogger, newws.New(), targetSelectHandler)

	// Setup our exponential backoff parameters
	backoffParams := backoff.NewExponentialBackOff()
	backoffParams.MaxElapsedTime = time.Hour * 72 // Wait in total at most 72 hours
	backoffParams.MaxInterval = time.Minute * 15  // At most 15 minutes in between requests

	ticker := backoff.NewTicker(backoffParams)
	for {
		select {
		case <-w.tmb.Dying():
			return nil
		case tick, ok := <-ticker.C:
			if !ok {
				return fmt.Errorf("failed to connect after %s", backoffParams.MaxElapsedTime)
			}

			if w.getChallenge {
				// First get the config from the vault
				config, err := vault.LoadVault()
				if err != nil {
					return fmt.Errorf("failed to retrieve agent vault: %s", err)
				}

				solvedChallenge, err := challenge.Get(w.connectionUrl.String(), w.params["target_id"][0], w.params["version"][0], config.Data.PrivateKey)
				if err != nil {
					w.params["solved_challenge"] = []string{solvedChallenge}
				}

				// And sign our agent version
				// LUCIE: this is a bit messy right now but with Sebby's changes this should streamline signing
				if signedAgentVersion, err := challenge.Solve(w.params["version"][0], config.Data.PrivateKey); err != nil {
					return fmt.Errorf("error signing agent version: %s", err)
				} else {
					w.params["signed_agent_version"] = []string{signedAgentVersion}
				}
			}

			if err := conn.Connect(w.connectionUrl.String(), endpoint, w.params); err != nil {
				w.logger.Errorf("retrying in %s because of and error on connect: %w", backoffParams.NextBackOff().Round(time.Second), err)
			} else {
				w.logger.Info("Connection successful!")
				w.ready = true
				return nil
			}

			w.logger.Infof("failed to connect at %s. Will try again in about %s", tick, backoffParams.NextBackOff())
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

func (w *Websocket) createInvocationId(agentMessage am.AgentMessage) string {
	w.invocationIdLock.Lock()
	defer w.invocationIdLock.Unlock()

	w.currentInvocationId++
	currentInvocationIdStr := strconv.FormatInt(w.currentInvocationId, 10)

	w.messagesWaitingResponse[currentInvocationIdStr] = agentMessage

	return currentInvocationIdStr
}

func (w *Websocket) deleteInvocationId(invocationId string) {
	w.invocationIdLock.Lock()
	defer w.invocationIdLock.Unlock()

	delete(w.messagesWaitingResponse, invocationId)
}

func (w *Websocket) getAgentMessageFromInvocationId(invocationId string) am.AgentMessage {
	w.invocationIdLock.Lock()
	defer w.invocationIdLock.Unlock()

	return w.messagesWaitingResponse[invocationId]
}

// can be used by other processes to check if our connection is open
func (w *Websocket) Ready() bool {
	return w.ready
}

func (w *Websocket) waitForAgentWebsocketReady() {
	// If agent websocket is already ready return right away
	if w.sendQueueReady {
		return
	}

	// If this is a daemon websocket connection wait for the agent to
	// connect before sending any messages from the output queue
	if w.myType == DaemonWebsocket {
		select {
		case <-w.agentReadyChan:
			w.sendQueueReady = true
		case <-time.After(AgentConnectedWebsocketTimeout):
			w.Close(fmt.Errorf("Timed out waiting for agent websocket to connect"))
		}
	} else {
		w.sendQueueReady = true
	}
}
