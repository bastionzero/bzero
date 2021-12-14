package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"github.com/gorilla/websocket"
)

// Declaring flags as package-accessible variables
var (
	sessionId, authHeader, serviceUrl string
	daemonPort, logPath               string
)

const (
	hubEndpoint                  = "/api/v1/hub/db/daemon"
	version                      = "$DAEMON_VERSION"
	signalRMessageTerminatorByte = 0x1E
	signalRTypeNumber            = 1 // Ref: https://github.com/aspnet/SignalR/blob/master/specs/HubProtocol.md#invocation-message-encoding
)

type AgentMessage struct {
	ChannelId      string `json:"ChannelId"` // acts like a session id to tie messages to a keysplitting hash chain
	MessageType    string `json:"messageType"`
	SchemaVersion  string `json:"schemaVersion" default:"1.0"`
	MessagePayload []byte `json:"messagePayload"`
}

type SignalRWrapper struct {
	Target    string         `json:"target"` // hub name
	Type      int            `json:"type"`
	Arguments []AgentMessage `json:"arguments"`
}

func main() {
	// First parse any flags
	parseFlags()

	// Create our logger
	logger, err := logger.New(logger.Debug, getLogFilePath())
	if err != nil {
		os.Exit(1)
	}
	logger.AddDaemonVersion(version)

	// Now create our websocket connection
	params := make(map[string]string)
	params["session_id"] = sessionId

	// Create our headers and params
	headers := make(map[string]string)
	headers["Authorization"] = authHeader

	// Make our POST request
	negotiateEndpoint := "https://" + serviceUrl + hubEndpoint + "/negotiate"
	logger.Infof("Starting negotiation with endpoint %s", negotiateEndpoint)

	if response, err := bzhttp.Post(logger, negotiateEndpoint, "application/json", []byte{}, headers, params); err != nil {
		logger.Error(fmt.Errorf("error on negotiation: %s. Response: %+v", err, response))
	} else {

		// Extract out the connection token
		bodyBytes, _ := ioutil.ReadAll(response.Body)
		var m map[string]interface{}

		if err := json.Unmarshal(bodyBytes, &m); err != nil {
			// TODO: Add error handling around this, we should at least retry and then bubble up the error to the user
			logger.Error(fmt.Errorf("error un-marshalling negotiate response: %+v", m))
		}

		// Add the connection id to the list of params
		params["id"] = m["connectionId"].(string)
		params["clientProtocol"] = "1.5"
		params["transport"] = "WebSockets"

		logger.Info("Negotiation successful")
		response.Body.Close()
	}

	// Build our url u , add our params as well
	websocketUrl := url.URL{Scheme: "wss", Host: serviceUrl, Path: hubEndpoint}
	logger.Infof("Connecting to %s", websocketUrl.String())

	q := websocketUrl.Query()
	for key, value := range params {
		q.Set(key, value)
	}
	websocketUrl.RawQuery = q.Encode()

	client, _, err := websocket.DefaultDialer.Dial(websocketUrl.String(), http.Header{"Authorization": []string{headers["Authorization"]}})
	if err != nil {
		logger.Error(err)
	} else {
		// Define our protocol and version
		// Ref: https://stackoverflow.com/questions/65214787/signalr-websockets-and-go
		if err := client.WriteMessage(websocket.TextMessage, append([]byte(`{"protocol": "json","version": 1}`), signalRMessageTerminatorByte)); err != nil {
			logger.Info("Error when trying to agree on version for SignalR!")
			client.Close()
		} else {
			logger.Info("Connection successful!")
		}
	}

	// Now create our local listener for TCP connections
	laddr, err := net.ResolveTCPAddr("tcp", ":5432")
	listener, err := net.ListenTCP("tcp", laddr)
	if err != nil {
		logger.Errorf("Failed to open local port to listen: %s", err)
		os.Exit(1)
	}

	// Now keep listening for new tcp events
	for {
		conn, err := listener.AcceptTCP()
		if err != nil {
			logger.Errorf("Failed to accept connection '%s'", err)
			continue
		}

		go handleProxy(conn, logger, client)
	}
}

func handleProxy(lconn *net.TCPConn, logger *logger.Logger, client *websocket.Conn) {
	// Always ensure we close the local tcp connection
	defer lconn.Close()

	// Keep looping till we hit EOF
	tmp := make([]byte, 256)
	for {
		n, err := lconn.Read(tmp)
		if err != nil {
			logger.Errorf("Read failed '%s'\n", err)
			return
		}
		buff := tmp[:n]

		signalRMessage := SignalRWrapper{
			Target: "RequestDaemonToBastionV1",
			Type:   signalRTypeNumber,
			Arguments: []AgentMessage{{
				ChannelId:      "test",
				MessageType:    "keysplitting",
				SchemaVersion:  "v1",
				MessagePayload: buff,
			}},
		}

		// Write our message to websocket
		if msgBytes, err := json.Marshal(signalRMessage); err != nil {
			logger.Error(fmt.Errorf("error marshalling outgoing SignalR Message: %v", signalRMessage))
		} else {
			if err := client.WriteMessage(websocket.TextMessage, append(msgBytes, signalRMessageTerminatorByte)); err != nil {
				logger.Error(err)
			}
			logger.Info("ARE WE HERE?")
		}
	}
}

func parseFlags() error {
	flag.StringVar(&sessionId, "sessionId", "", "Session ID From Zli")
	flag.StringVar(&authHeader, "authHeader", "", "Auth Header From Zli")

	// Our expected flags we need to start
	flag.StringVar(&serviceUrl, "serviceURL", "", "Service URL to use")

	// Plugin variables
	flag.StringVar(&daemonPort, "daemonPort", "", "Daemon Port To Use")
	flag.StringVar(&logPath, "logPath", "", "Path to log file for daemon")

	flag.Parse()

	// Check we have all required flags
	if sessionId == "" || authHeader == "" || serviceUrl == "" ||
		daemonPort == "" || logPath == "" {
		return fmt.Errorf("missing flags")
	}

	return nil
}

func getLogFilePath() string {
	return logPath
}
