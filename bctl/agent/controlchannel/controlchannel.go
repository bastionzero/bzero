package controlchannel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"bastionzero.com/bctl/v1/bctl/agent/controlchannel/tracker"
	"bastionzero.com/bctl/v1/bctl/agent/datachannel"
	"bastionzero.com/bctl/v1/bctl/agent/datachannel/connectionhub"
	"bastionzero.com/bctl/v1/bctl/agent/keysplitting"
	"bastionzero.com/bctl/v1/bctl/agent/vault"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	newsignalr "bastionzero.com/bctl/v1/bzerolib/connections/signalr"
	newws "bastionzero.com/bctl/v1/bzerolib/connections/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"

	"gopkg.in/tomb.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	heartRate = 20 * time.Second

	// Hub for our agent datachannel on the connection node
	agentDataChannelEndpoint = "hub/agent"
)

type ControlChannel struct {
	websocket websocket.IWebsocket
	logger    *logger.Logger
	tmb       tomb.Tomb
	id        string

	// config values needed for keysplitting
	ksConfig keysplitting.IKeysplittingConfig

	// variables for opening websockets
	serviceUrl string

	tracker tracker.ConnectionsTracker

	// These are all the types of channels we have available
	inputChan chan am.AgentMessage

	targetType string
}

func Start(logger *logger.Logger,
	id string,
	websocket websocket.IWebsocket, // control channel websocket
	serviceUrl string,
	targetType string,
	ksConfig keysplitting.IKeysplittingConfig) (*ControlChannel, error) {

	control := &ControlChannel{
		websocket:  websocket,
		logger:     logger,
		id:         id,
		ksConfig:   ksConfig,
		serviceUrl: serviceUrl,
		targetType: targetType,
		inputChan:  make(chan am.AgentMessage, 25),
		tracker:    *tracker.New(),
	}

	// The ChannelId is mostly for distinguishing multiple channels over a single websocket but the control channel has
	// its own dedicated websocket.  This also makes it so there can only ever be one control channel associated with a
	// given websocket at any time.
	// TODO: figure out a way to let control channel know its own id before it subscribes
	websocket.Subscribe("", control)

	// Set up our handler to deal with incoming messages
	control.tmb.Go(func() error {
		defer websocket.Unsubscribe(id)

		// send healthcheck messages at every "heartbeat"
		control.tmb.Go(func() error {
			ticker := time.NewTicker(heartRate)
			defer ticker.Stop()
			for {
				select {
				case <-control.tmb.Dying():
					logger.Info("Ceasing heartbeats")
					return nil
				case <-ticker.C:
					// don't bother trying to send heartbeats if we're not connected
					if websocket.Ready() {
						if msg, err := control.checkHealth(); err != nil {
							control.logger.Errorf("error creating healthcheck message: %s", err)
						} else {
							control.send(am.HealthCheck, msg)
						}
					}
				}
			}
		})

		for {
			select {
			case <-control.tmb.Dying():
				// We need to close all open client connections if the control channel has been closed
				logger.Info("Closing all agent connections since control channel has been closed")

				for _, conn := range control.tracker.GetAllConnections() {
					// First send a close message over the agent websocket
					conn.Receive(am.AgentMessage{
						MessageType:    string(am.CloseDaemonWebsocket),
						MessagePayload: []byte{},
						SchemaVersion:  am.CurrentVersion,
						ChannelId:      "-1", // Channel Id does not since this applies to all datachannels
					})

					// Then close the websocket
					conn.Close(fmt.Errorf("control channel has been closed"))
				}
				return nil
			case agentMessage := <-control.inputChan:
				// Process each message in its own thread
				go func() {
					if err := control.processInput(agentMessage); err != nil {
						logger.Error(err)
					}
				}()
			}
		}
	})

	return control, nil
}

func (c *ControlChannel) Close(reason error) {
	c.tmb.Kill(reason)
	c.tmb.Wait()
}

func (c *ControlChannel) Receive(agentMessage am.AgentMessage) {
	c.inputChan <- agentMessage
}

// Wraps and sends the payload
func (c *ControlChannel) send(messageType am.MessageType, messagePayload interface{}) error {
	c.logger.Debugf("control channel is sending %s message", messageType)
	messageBytes, _ := json.Marshal(messagePayload)
	agentMessage := am.AgentMessage{
		ChannelId:      c.id,
		MessageType:    string(messageType),
		SchemaVersion:  am.CurrentVersion,
		MessagePayload: messageBytes,
	}

	// Push message to websocket channel output
	c.websocket.Send(agentMessage)
	return nil
}

func (c *ControlChannel) createConnection(message OpenWebsocketMessage) error {
	wsLogger := c.logger.GetWebsocketLogger(message.ConnectionId)
	srLogger := wsLogger.GetComponentLogger("SignalR")

	// LUCIE: ooooh nooooo. mixture of camelCase and underscores in type definitions...
	params := map[string][]string{
		"connection_id":  {message.ConnectionId},
		"token":          {message.Token},
		"connectionType": {message.Type},
	}

	connection, err := newsignalr.New(srLogger, newws.New(), message.ConnectionServiceUrl, agentDataChannelEndpoint, params)
	if err != nil {
		return fmt.Errorf("failed to create new connection to %s: %w", message.ConnectionServiceUrl, err)
	}

	c.tracker.TrackNewConnection(message.ConnectionId, connection)
	return nil
}

func (c *ControlChannel) createDataChannel(message OpenDataChannelMessage) error {
	// Setup all of our loggers
	dcLogger := c.logger.GetDatachannelLogger(message.DataChannelId)
	chLogger := c.logger.GetComponentLogger("connection")
	ksLogger := dcLogger.GetComponentLogger("mrzap")

	// Get our existing connection for the datachannel to attach to
	conn, ok := c.tracker.GetConnection(message.ConnectionId)
	if !ok {
		return fmt.Errorf("the provided connection id %s does not match any existing agent-made connection", message.ConnectionId)
	}

	// Create a connection hub for the data channel to listen and send on
	connectionHub := connectionhub.New(chLogger, &c.tmb)
	connectionHub.Attach(message.ConnectionId, conn)

	keysplitter, err := keysplitting.New(ksLogger, c.ksConfig)
	if err != nil {
		return err
	}

	_, err = datachannel.New(dcLogger, connectionHub, keysplitter, message.DataChannelId, message.Syn)
	if err != nil {
		return err
	}

	c.tracker.TrackNewDataChannel(message.ConnectionId, message.DataChannelId, connectionHub)
	return nil
}

// This is our main process function where incoming messages from the websocket will be processed
func (c *ControlChannel) processInput(agentMessage am.AgentMessage) error {
	c.logger.Debugf("control channel received %v message", am.MessageType(agentMessage.MessageType))

	switch am.MessageType(agentMessage.MessageType) {
	case am.HealthCheck:
		c.logger.Debugf("as of version 4.2.0 this agent no longer accepts healthcheck messages; ignoring")
		return nil
	case am.OpenWebsocket:
		var owRequest OpenWebsocketMessage
		if err := json.Unmarshal(agentMessage.MessagePayload, &owRequest); err != nil {
			return fmt.Errorf("malformed open websocket request: %s", err)
		} else {
			return c.createConnection(owRequest)
		}
	case am.CloseWebsocket:
		var cwRequest CloseWebsocketMessage
		if err := json.Unmarshal(agentMessage.MessagePayload, &cwRequest); err != nil {
			return fmt.Errorf("malformed close websocket request")
		} else {
			if conn, ok := c.tracker.GetConnection(cwRequest.ConnectionId); ok {
				// this can take a little time, but we don't want it blocking other things
				go func() {
					conn.Close(errors.New("connection closed on daemon"))
				}()
			} else {
				return fmt.Errorf("could not close non existent websocket with id: %s", cwRequest.ConnectionId)
			}
		}
	case am.OpenDataChannel:
		var odRequest OpenDataChannelMessage
		if err := json.Unmarshal(agentMessage.MessagePayload, &odRequest); err != nil {
			return fmt.Errorf("malformed open datachannel request: %s", err)
		} else {
			if err := c.createDataChannel(odRequest); err != nil {
				return fmt.Errorf("error creating datachannel: %s", err)
			}
		}
	case am.CloseDataChannel:
		var cdRequest CloseDataChannelMessage
		if err := json.Unmarshal(agentMessage.MessagePayload, &cdRequest); err != nil {
			return fmt.Errorf("malformed close datachannel request: %s", err)
		} else {
			if hub, ok := c.tracker.GetDataChannel(cdRequest.ConnectionId, cdRequest.DataChannelId); ok {
				hub.Close()
				c.tracker.UntrackDataChannel(cdRequest.ConnectionId, cdRequest.DataChannelId)
			} else {
				return err
			}
		}
	default:
		return fmt.Errorf("unrecognized message type: %s", agentMessage.MessageType)
	}

	return nil
}

func (c *ControlChannel) checkHealth() (AliveCheckAgentToBastionMessage, error) {
	// Let bastion know a list of valid cluster roles
	// Create our api object
	if vault.InCluster() {
		return checkInClusterHealth()
	}

	return AliveCheckAgentToBastionMessage{
		Alive:        true,
		ClusterUsers: []string{},
	}, nil
}

func checkInClusterHealth() (AliveCheckAgentToBastionMessage, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return AliveCheckAgentToBastionMessage{}, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return AliveCheckAgentToBastionMessage{}, err
	}

	// Then get all cluster roles
	clusterRoleBindings, err := clientset.RbacV1().ClusterRoleBindings().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return AliveCheckAgentToBastionMessage{}, err
	}

	clusterUsers := make(map[string]bool)

	for _, clusterRoleBinding := range clusterRoleBindings.Items {
		// Now loop over the subjects if we can find any user subjects
		for _, subject := range clusterRoleBinding.Subjects {
			if subject.Kind == "User" {
				// We do not consider any system:... or eks:..., basically any system: looking roles as valid. This can be overridden from Bastion
				var systemRegexPatten = regexp.MustCompile(`[a-zA-Z0-9]*:[a-za-zA-Z0-9-]*`)
				if !systemRegexPatten.MatchString(subject.Name) {
					clusterUsers[subject.Name] = true
				}
			}
		}
	}

	// Then get all roles
	roleBindings, err := clientset.RbacV1().RoleBindings("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return AliveCheckAgentToBastionMessage{}, err
	}

	for _, roleBindings := range roleBindings.Items {
		// Now loop over the subjects if we can find any user subjects
		for _, subject := range roleBindings.Subjects {
			if subject.Kind == "User" {
				// We do not consider any system:... or eks:..., basically any system: looking roles as valid. This can be overridden from Bastion
				var systemRegexPatten = regexp.MustCompile(`[a-zA-Z0-9]*:[a-za-zA-Z0-9-]*`) // TODO: double check
				if !systemRegexPatten.MatchString(subject.Name) {
					clusterUsers[subject.Name] = true
				}
			}
		}
	}

	// Now build our response
	users := []string{}
	for key := range clusterUsers {
		users = append(users, key)
	}

	return AliveCheckAgentToBastionMessage{
		Alive:        true,
		ClusterUsers: users,
	}, nil
}
