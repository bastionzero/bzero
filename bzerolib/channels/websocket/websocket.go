package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	backoff "github.com/cenkalti/backoff/v4"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/agent/vault"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket/challenge"
	"bastionzero.com/bctl/v1/bzerolib/connection/broker"
	"bastionzero.com/bctl/v1/bzerolib/connection/httpclient"
	"bastionzero.com/bctl/v1/bzerolib/connection/signalr"
	newws "bastionzero.com/bctl/v1/bzerolib/connection/websocket"
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
	agentConnected        = "AgentConnected"
	daemonCloseConnection = "CloseConnection"
)

const (
	// Hub endpoints
	daemonHubEndpoint = "hub/daemon"
	agentHubEndpoint  = "hub/agent"

	controlHubEndpoint = "/api/v1/hub/control"

	AgentConnectedWebsocketTimeout = 30 * time.Second
	closeConnectionEndpoint        = "/api/v2/connections/$ID/close"
)

// This will be the client that we use to store our websocket connection
type Websocket struct {
	tmb    tomb.Tomb
	logger *logger.Logger

	client *signalr.SignalR

	broker *broker.Broker

	// Buffered channel to keep track of outgoing messages
	sendQueue chan *am.AgentMessage

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
		logger:         logger,
		broker:         broker.New(),
		sendQueue:      make(chan *am.AgentMessage, 50),
		myType:         wtype,
		agentReadyChan: make(chan struct{}),
	}

	// Connect to the websocket in a go routine in case it takes a long time
	go func() {
		if err := ws.connect(u, headers, params); err != nil {
			logger.Error(err)
			ws.Close(fmt.Errorf("process was unable to connect to BastionZero"))

			// If this is a daemon connection (i.e. we are not getting a challenge)
			// we also need to make sure we close the connection in the backend
			if ws.myType == AgentWebsocket || ws.myType == DaemonWebsocket {
				if err := ws.closeConnection(bastionUrl, params); err != nil {
					ws.logger.Errorf("failed to close connection: %w", err)
				}
			}
		}
	}()

	ws.tmb.Go(func() error {

		// Listener for any messages that need to be sent
		ws.tmb.Go(func() error {
			defer ws.Close(ws.tmb.Err())
			for {
				select {
				case <-ws.tmb.Dying():
					return nil
				case message := <-ws.sendQueue:
					ws.waitForAgentWebsocketReady()
					ws.client.Receive(*message)
				}
			}
		})

		// Receive any messages in the websocket
		for {
			select {
			case <-ws.tmb.Dying():
				return nil
			case <-ws.client.Done():
				if autoReconnect {
					if err := ws.connect(u, headers, params); err != nil {
						logger.Error(err)

						// If this is a daemon connection (i.e. we are not getting a challenge)
						// we also need to make sure we close the connection in the backend
						if ws.myType == AgentWebsocket || ws.myType == DaemonWebsocket {
							if err := ws.closeConnection(bastionUrl, params); err != nil {
								ws.logger.Errorf("failed to close connection: %w", err)
							}
						}

						return fmt.Errorf("process was unable to reconnect to BastionZero after being disconnected")
					}
				} else {
					return fmt.Errorf("connection has been lost and we're not retrying")
				}
			case message := <-ws.client.Inbound():
				if err := ws.receive(*message); err != nil {
					ws.logger.Error(err)
				}
			}
		}
	})

	return &ws, nil
}

func (w *Websocket) Close(reason error) {
	if !w.tmb.Alive() {
		return
	}

	w.logger.Infof("websocket closing because: %s", reason)

	// close all of our existing datachannels
	w.broker.Close(reason)

	// close the websocket connection. This will cause errors when reading from
	// websocket in receive
	if w.client != nil {
		w.client.Close(reason)
	}

	w.tmb.Kill(reason)
	w.tmb.Wait()
}

// add channel to channels dictionary for forwarding incoming messages
func (w *Websocket) Subscribe(id string, channel broker.IChannel) {
	w.broker.Subscribe(id, channel)
}

// remove channel from channel dictionary
func (w *Websocket) Unsubscribe(id string) {
	w.broker.Unsubscribe(id)
}

// Returns error on websocket closed
func (w *Websocket) receive(message signalr.SignalRMessage) error {
	switch message.Target {
	case daemonCloseConnection:
		rerr := errors.New("the bzero agent terminated the connection")
		w.Close(rerr)
		return rerr
	case agentConnected:
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
		// Otherwise assume that the invocation contains a single AgentMessage argument
		if len(message.Arguments) != 1 {
			return fmt.Errorf("expected a single agent message argument but got %d arguments", len(message.Arguments))
		}

		var agentMessage am.AgentMessage
		if err := json.Unmarshal(message.Arguments[0], &agentMessage); err != nil {
			return fmt.Errorf("error unmarshalling agent message from websocket method %s: %w", message.Target, err)
		}

		if err := w.broker.Narrowcast(agentMessage.ChannelId, agentMessage); err != nil {
			w.logger.Errorf("failed to forward agent message to datachannel: %w", err)
		}
	}
	return nil
}

// Function to write signalr message to websocket
func (w *Websocket) Send(agentMessage am.AgentMessage) {
	w.sendQueue <- &agentMessage
}

// Opens a websocket connection to Bastion
//
// in order for Connect to serve as a robust abstraction for other processes that rely on it,
// it must handle its own retry logic. For this, we use an exponential backoff. Some failures
// within the connection process are considered transient, and thus trigger a retry. Others are
// considered fatal, and return an error
func (w *Websocket) connect(connectionUrl *url.URL, headers map[string][]string, params map[string][]string) error {
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

			if w.myType == AgentControl {
				// First get the config from the vault
				config, err := vault.LoadVault()
				if err != nil {
					return fmt.Errorf("failed to retrieve agent vault: %s", err)
				}

				solvedChallenge, err := challenge.Get(connectionUrl.String(), params["target_id"][0], params["version"][0], config.Data.PrivateKey)
				if err != nil {
					params["solved_challenge"] = []string{solvedChallenge}
				}

				// And sign our agent version
				// LUCIE: this is a bit messy right now but with Sebby's changes this should streamline signing
				if signedAgentVersion, err := challenge.Solve(params["version"][0], config.Data.PrivateKey); err != nil {
					return fmt.Errorf("error signing agent version: %s", err)
				} else {
					params["signed_agent_version"] = []string{signedAgentVersion}
				}
			}

			if err := conn.Connect(connectionUrl.String(), endpoint, params); err != nil {
				w.logger.Errorf("retrying in %s because of and error on connect: %w", backoffParams.NextBackOff().Round(time.Second), err)
			} else {
				w.logger.Info("Connection successful!")
				return nil
			}

			w.logger.Infof("failed to connect at %s. Will try again in about %s", tick, backoffParams.NextBackOff())
		}
	}
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
			w.Close(fmt.Errorf("timed out waiting for agent websocket to connect"))
		}
	} else {
		w.sendQueueReady = true
	}
}

func (w *Websocket) closeConnection(bastionUrl string, params map[string][]string) error {
	var connectionId string
	switch w.myType {
	case AgentWebsocket:
		connectionId = params["connection_id"][0]
	case DaemonWebsocket:
		connectionId = params["connectionId"][0]
	}

	endpoint := strings.Replace(closeConnectionEndpoint, "$ID", connectionId, -1)

	options := httpclient.HTTPOptions{
		Endpoint: endpoint,
	}
	client, err := httpclient.NewWithBackoff(w.logger, bastionUrl, options)
	if err != nil {
		return err
	}
	_, err = client.Patch(context.Background())
	if err != nil {
		return fmt.Errorf("error closing connection: %s", err)
	}

	return nil
}
