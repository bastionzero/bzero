package controlchannel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"

	"bastionzero.com/bctl/v1/bctl/agent/datachannel"
	"bastionzero.com/bctl/v1/bctl/agent/vault"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"

	"gopkg.in/tomb.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type wsMeta struct {
	Client       *websocket.Websocket
	DataChannels map[string]websocket.IChannel
}
type ControlChannel struct {
	websocket *websocket.Websocket
	logger    *logger.Logger
	tmb       tomb.Tomb
	id        string

	// variables for opening websockets
	serviceUrl            string
	hubEndpoint           string
	dcTargetSelectHandler func(msg am.AgentMessage) (string, error)

	// These are all the types of channels we have available
	inputChan chan am.AgentMessage

	// struct for keeping track of all connections key'ed with connectionId (websockets with associated datachannels)
	connections     map[string]wsMeta
	connectionsLock sync.Mutex

	SocketLock sync.Mutex // Ref: https://github.com/gorilla/websocket/issues/119#issuecomment-198710015
}

func Start(logger *logger.Logger,
	id string,
	websocket *websocket.Websocket, // control channel websocket
	serviceUrl string,
	hubEndpoint string,
	targetSelectHandler func(msg am.AgentMessage) (string, error)) error {

	control := &ControlChannel{
		websocket: websocket,
		logger:    logger,
		id:        id,

		serviceUrl:            serviceUrl,
		hubEndpoint:           hubEndpoint,
		dcTargetSelectHandler: targetSelectHandler,

		inputChan: make(chan am.AgentMessage, 25),

		connections: make(map[string]wsMeta),
	}

	// The ChannelId is mostly for distinguishing multiple channels over a single websocket but the control channel has
	// its own dedicated websocket.  This also makes it so there can only ever be one control channel associated with a
	// given websocket at any time.
	websocket.Subscribe("", control)

	// Set up our handler to deal with incoming messages
	control.tmb.Go(func() error {
		defer websocket.Unsubscribe(id)
		for {
			select {
			case <-control.tmb.Dying():
				return nil
			case agentMessage := <-control.inputChan:
				if err := control.processInput(agentMessage); err != nil {
					logger.Error(err)
				}
			}
		}
	})

	return nil
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
	c.logger.Tracef("control channel is sending %s message", messageType)
	messageBytes, _ := json.Marshal(messagePayload)
	agentMessage := am.AgentMessage{
		ChannelId:      c.id,
		MessageType:    string(messageType),
		SchemaVersion:  am.SchemaVersion,
		MessagePayload: messageBytes,
	}

	// Push message to websocket channel output
	c.websocket.Send(agentMessage)
	return nil
}

func (c *ControlChannel) openWebsocket(message OpenWebsocketMessage) error {
	subLogger := c.logger.GetWebsocketLogger(message.ConnectionId)

	// Create our headers and params, headers are empty
	headers := make(map[string]string)

	// Add our token to our params
	params := make(map[string]string)
	params["daemon_connection_id"] = message.ConnectionId
	params["token"] = message.Token

	if ws, err := websocket.New(subLogger, message.ConnectionId, c.serviceUrl, c.hubEndpoint, params, headers, c.dcTargetSelectHandler, false, false); err != nil {
		return fmt.Errorf("could not create new websocket: %s", err)
	} else {
		// add the websocket to our connections dictionary
		c.logger.Infof("created websocket with id: %s", message.ConnectionId)
		meta := wsMeta{
			Client:       ws,
			DataChannels: make(map[string]websocket.IChannel),
		}
		c.updateConnectionsMap(message.ConnectionId, meta)
	}
	return nil
}

func (c *ControlChannel) openDataChannel(message OpenDataChannelMessage) error {
	wsId := message.ConnectionId
	dcId := message.DataChannelId

	subLogger := c.logger.GetDatachannelLogger(dcId)

	// grab the websocket
	if websocketMeta, ok := c.getConnectionMap(wsId); !ok {
		return fmt.Errorf("agent does not have a websocket associated with id %s", wsId)
	} else {
		if datachannel, err := datachannel.New(subLogger, &c.tmb, websocketMeta.Client, message.TargetUser, message.TargetGroups, dcId); err != nil {
			return err
		} else {
			// add our new datachannel to our connections dictionary
			websocketMeta.DataChannels[dcId] = datachannel

			// let the daemon know the datachannel is ready
			websocketMeta.Client.Send(am.AgentMessage{
				ChannelId:   dcId,
				MessageType: string(am.DataChannelReady),
			})
		}
	}
	return nil
}

// This is our main process function where incoming messages from the websocket will be processed
func (c *ControlChannel) processInput(agentMessage am.AgentMessage) error {
	c.logger.Debugf("control channel received %v message", am.MessageType(agentMessage.MessageType))

	switch am.MessageType(agentMessage.MessageType) {
	case am.HealthCheck:
		var healthCheckMessage HealthCheckMessage
		if err := json.Unmarshal(agentMessage.MessagePayload, &healthCheckMessage); err != nil {
			return fmt.Errorf("malformed check health request: %s", err)
		} else {
			// send message to be processed
			if msg, err := checkHealth(healthCheckMessage); err != nil {
				return fmt.Errorf("error processing health check message: %s", err)
			} else {
				c.send(am.HealthCheck, msg)
			}
		}
	case am.OpenWebsocket:
		var owRequest OpenWebsocketMessage
		if err := json.Unmarshal(agentMessage.MessagePayload, &owRequest); err != nil {
			return fmt.Errorf("malformed open websocket request: %s", err)
		} else {
			return c.openWebsocket(owRequest)
		}
	case am.CloseWebsocket:
		var cwRequest CloseWebsocketMessage
		if err := json.Unmarshal(agentMessage.MessagePayload, &cwRequest); err != nil {
			return fmt.Errorf("malformed close websocket request")
		} else {
			if websocket, ok := c.getConnectionMap(cwRequest.ConnectionId); ok {
				// this can take a little time, but we don't want it blocking other things
				go func() {
					websocket.Client.Close(errors.New("websocket closed on daemon"))
					c.deleteConnectionsMap(cwRequest.ConnectionId)
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
			if err := c.openDataChannel(odRequest); err != nil {
				return fmt.Errorf("error creating datachannel: %s", err)
			}
		}
	case am.CloseDataChannel:
		var cdRequest CloseDataChannelMessage
		if err := json.Unmarshal(agentMessage.MessagePayload, &cdRequest); err != nil {
			return fmt.Errorf("malformed close datachannel request: %s", err)
		} else {
			if websocket, ok := c.connections[cdRequest.ConnectionId]; ok {
				if datachannel, ok := websocket.DataChannels[cdRequest.DataChannelId]; ok {
					datachannel.Close(errors.New("formal datachannel request received"))
					delete(websocket.DataChannels, cdRequest.DataChannelId)
				} else {
					return fmt.Errorf("agent does not have a datachannel with id: %s", cdRequest.DataChannelId)
				}
			} else {
				return fmt.Errorf("agent does not have a websocket with id: %s", cdRequest.ConnectionId)
			}
		}
	default:
		return fmt.Errorf("unrecognized message type: %s", agentMessage.MessageType)
	}

	return nil
}

func checkHealth(healthCheckMessage HealthCheckMessage) (AliveCheckClusterToBastionMessage, error) {
	// Load in our saved config
	secretData, err := vault.LoadVault()
	if err != nil {
		return AliveCheckClusterToBastionMessage{}, err
	}

	// Update the vault value
	secretData.Data.ClusterName = healthCheckMessage.ClusterName
	secretData.Save()

	// Also let bastion know a list of valid cluster roles
	// Create our api object
	config, err := rest.InClusterConfig()
	if err != nil {
		return AliveCheckClusterToBastionMessage{}, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return AliveCheckClusterToBastionMessage{}, err
	}

	// Then get all cluster roles
	clusterRoleBindings, err := clientset.RbacV1().ClusterRoleBindings().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return AliveCheckClusterToBastionMessage{}, err
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
		return AliveCheckClusterToBastionMessage{}, err
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

	return AliveCheckClusterToBastionMessage{
		Alive:        true,
		ClusterUsers: users,
	}, nil
}

// Helper function so we avoid writing to this map at the same time
func (c *ControlChannel) updateConnectionsMap(id string, newWS wsMeta) {
	c.connectionsLock.Lock()
	c.connections[id] = newWS
	c.connectionsLock.Unlock()
}

func (c *ControlChannel) deleteConnectionsMap(id string) {
	c.connectionsLock.Lock()
	delete(c.connections, id)
	c.connectionsLock.Unlock()
}

func (c *ControlChannel) getConnectionMap(id string) (wsMeta, bool) {
	c.connectionsLock.Lock()
	defer c.connectionsLock.Unlock()
	meta, ok := c.connections[id]
	return meta, ok
}
