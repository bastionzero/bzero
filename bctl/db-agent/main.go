package main

import (
	"bytes"
	"encoding/json"
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
// var (
// 	sessionId, authHeader, serviceUrl string
// 	daemonPort, logPath               string
// )

const (
	hubEndpoint                  = "/api/v1/hub/db/agent"
	serviceUrl                   = "sid.bastionzero.com"
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
	// parseFlags()

	logger, err := logger.New(logger.Debug, "test.log")
	if err != nil {
		os.Exit(1)
	}
	logger.AddDaemonVersion(version)

	// Now create headers
	headers := make(map[string]string)

	// Now create our websocket connection
	params := make(map[string]string)
	params["daemon_connection_id"] = "xgovFibyxjE6kC1PNO2pdA"

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

	// Open up a connection to the TCP addr we are trying to connect to
	raddr, err := net.ResolveTCPAddr("tcp", ":8000")
	if err != nil {
		logger.Errorf("Failed to resolve remote address: %s", err)
		os.Exit(1)
	}

	rconn, err := net.DialTCP("tcp", nil, raddr)

	// Setup a go routine to listen for messages coming from this local connection
	go func() {
		buff := make([]byte, 0xffff)
		for {
			n, err := rconn.Read(buff)
			if err != nil {
				logger.Errorf("Read failed '%s'\n", err)
				return
			}
			b := buff[:n]

			logger.Infof("Received %d bytes from local tcp connection, sending to bastion", n)

			// Now send this to bastion
			signalRMessage := SignalRWrapper{
				Target: "ResponseAgentToBastionV1",
				Type:   signalRTypeNumber,
				Arguments: []AgentMessage{{
					ChannelId:      "test",
					MessageType:    "keysplitting",
					SchemaVersion:  "v1",
					MessagePayload: b,
				}},
			}

			// Write our message to websocket
			if msgBytes, err := json.Marshal(signalRMessage); err != nil {
				logger.Error(fmt.Errorf("error marshalling outgoing SignalR Message: %v", signalRMessage))
			} else {
				if err := client.WriteMessage(websocket.TextMessage, append(msgBytes, signalRMessageTerminatorByte)); err != nil {
					logger.Error(err)
				}
				logger.Info("Send message to Bastion")
			}
		}
	}()

	// Now listen for messages from bastion
	for {
		// Read incoming message(s)
		_, rawMessage, err := client.ReadMessage()

		if err != nil {
			logger.Error(err)
			return
		} else {
			// Unwrap the message
			if messages, err := unwrapSignalR(rawMessage, logger); err != nil {
				logger.Error(err)
			} else {
				for _, message := range messages {
					logger.Infof("HERE: %s", message.Target)

					//write out result
					n, err := rconn.Write(message.Arguments[0].MessagePayload)
					if err != nil {
						logger.Errorf("Write failed '%s'\n", err)
						return
					}

					logger.Infof("Wrote %d bytes to local tcp connection", n)
				}
			}
		}
	}

}

func unwrapSignalR(rawMessage []byte, logger *logger.Logger) ([]SignalRWrapper, error) {
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
			logger.Trace(msg)
		} else if len(wrappedMessage.Arguments) != 0 { // make sure there is an AgentMessage
			messages = append(messages, wrappedMessage)
		}
	}
	return messages, nil
}

// func parseFlags() error {
// 	flag.StringVar(&sessionId, "sessionId", "", "Session ID From Zli")
// 	flag.StringVar(&authHeader, "authHeader", "", "Auth Header From Zli")

// 	// Our expected flags we need to start
// 	flag.StringVar(&serviceUrl, "serviceURL", "", "Service URL to use")

// 	// Plugin variables
// 	flag.StringVar(&daemonPort, "daemonPort", "", "Daemon Port To Use")
// 	flag.StringVar(&logPath, "logPath", "", "Path to log file for daemon")

// 	flag.Parse()

// 	// Check we have all required flags
// 	if sessionId == "" || authHeader == "" || serviceUrl == "" ||
// 		daemonPort == "" || logPath == "" {
// 		return fmt.Errorf("missing flags")
// 	}

// 	return nil
// }

// func getLogFilePath() string {
// 	return logPath
// }
