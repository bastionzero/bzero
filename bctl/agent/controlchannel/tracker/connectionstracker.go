package tracker

import (
	"fmt"
	"sync"

	"bastionzero.com/bctl/v1/bctl/agent/datachannel/connectionhub"
	"bastionzero.com/bctl/v1/bzerolib/connections/signalr"
)

type connectionMeta struct {
	Connection   *signalr.SignalR
	DataChannels map[string]*connectionhub.ConnectionHub
}

type ConnectionsTracker struct {
	lock sync.RWMutex

	connectionMap  map[string]connectionMeta
	dataChannelMap map[string]*connectionhub.ConnectionHub
}

func New() *ConnectionsTracker {
	return &ConnectionsTracker{
		connectionMap:  make(map[string]connectionMeta),
		dataChannelMap: make(map[string]*connectionhub.ConnectionHub),
	}
}

func (c *ConnectionsTracker) TrackNewConnection(connectionId string, connection *signalr.SignalR) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	if _, ok := c.connectionMap[connectionId]; ok {
		return fmt.Errorf("a connection with id %s already exists", connectionId)
	} else {
		c.connectionMap[connectionId] = connectionMeta{
			Connection:   connection,
			DataChannels: make(map[string]*connectionhub.ConnectionHub),
		}
	}

	return nil
}

func (c *ConnectionsTracker) TrackNewDataChannel(connectionId string, datachannelId string, hub *connectionhub.ConnectionHub) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	// Add data channel connection to connectionToDataChannelMap map
	if conn, ok := c.connectionMap[connectionId]; !ok {
		return fmt.Errorf("no connection with id %s exists", connectionId)
	} else if _, ok := conn.DataChannels[datachannelId]; ok {
		return fmt.Errorf("data channel with ID %s already exists and is tracked for connection ID %s", datachannelId, connectionId)
	} else {
		conn.DataChannels[datachannelId] = hub
	}

	// Now add our data channel to the reverse map
	if _, ok := c.dataChannelMap[datachannelId]; ok {
		return fmt.Errorf("a datachannel with id %s already exists", datachannelId)
	} else {
		c.dataChannelMap[datachannelId] = hub
	}

	return nil
}

func (c *ConnectionsTracker) GetAllConnections() []*signalr.SignalR {
	c.lock.RLock()
	defer c.lock.RUnlock()

	connections := []*signalr.SignalR{}
	for _, conn := range c.connectionMap {
		connections = append(connections, conn.Connection)
	}

	return connections
}

// LUCIE: When do we stop tracking connections?? When connection is dead and all datachannels are gone?
func (c *ConnectionsTracker) GetConnection(connectionId string) (*signalr.SignalR, bool) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	if conn, ok := c.connectionMap[connectionId]; ok {
		return conn.Connection, ok
	} else {
		return nil, ok
	}
}

func (c *ConnectionsTracker) GetDataChannel(connectionId string, datachannelId string) (*connectionhub.ConnectionHub, bool) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	if conn, ok := c.connectionMap[connectionId]; ok {
		hub, ok := conn.DataChannels[datachannelId]
		return hub, ok
	} else {
		return nil, ok
	}
}

func (c *ConnectionsTracker) UntrackDataChannel(connectionId string, datachannelId string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	// Remove data channel from connection map
	if conn, ok := c.connectionMap[connectionId]; ok {
		delete(conn.DataChannels, datachannelId)
	}
}
