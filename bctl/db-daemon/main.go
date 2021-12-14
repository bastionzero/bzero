package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
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
)

func main() {
	parseFlags()

	logger, err := logger.New(logger.Debug, getLogFilePath())
	if err != nil {
		os.Exit(1)
	}
	logger.AddDaemonVersion(version)

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

	// var err error
	if client, _, err := websocket.DefaultDialer.Dial(websocketUrl.String(), http.Header{"Authorization": []string{headers["Authorization"]}}); err != nil {
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
