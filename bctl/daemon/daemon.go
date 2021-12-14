package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"bastionzero.com/bctl/v1/bctl/daemon/httpserver"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

// Declaring flags as package-accessible variables
var (
	sessionId, authHeader, targetUser, targetClusterId, serviceUrl           string
	daemonPort, localhostToken, environmentId, certPath, keyPath, configPath string
	logPath, refreshTokenCommand, targetGroupsRaw                            string
	targetGroups                                                             []string
)

const (
	hubEndpoint = "/api/v1/hub/kube"
	version     = "$DAEMON_VERSION"
)

func main() {
	parseFlags() // TODO: Output missing args error

	// Setup our loggers
	// TODO: Pass in debug level as flag or put it in the config
	logger, err := logger.New(logger.Debug, getLogFilePath())
	if err != nil {
		os.Exit(1)
	}
	logger.AddDaemonVersion(version)

	logger.Infof("Opening websocket to Bastion: %s", serviceUrl)
	if err := startHTTPServer(logger); err != nil {
		logger.Error(err)
	}

	select {} // sleep forever?
}

func startHTTPServer(logger *logger.Logger) error {
	// Create our headers and params
	headers := make(map[string]string)
	headers["Authorization"] = authHeader

	// Add our token to our params
	params := make(map[string]string)
	params["session_id"] = sessionId
	params["target_user"] = targetUser
	params["target_groups"] = targetGroupsRaw
	params["target_cluster_id"] = targetClusterId
	params["environment_id"] = environmentId

	subLogger := logger.GetComponentLogger("httpserver")

	// TODO: I know this is insane, we need a config
	return httpserver.StartHTTPServer(subLogger,
		daemonPort,
		certPath,
		keyPath,
		refreshTokenCommand,
		configPath,
		targetUser,
		targetGroups,
		localhostToken,
		serviceUrl,
		hubEndpoint,
		params,
		headers,
		targetSelectHandler)
}

func targetSelectHandler(agentMessage am.AgentMessage) (string, error) {
	switch am.MessageType(agentMessage.MessageType) {
	case am.Keysplitting:
		return "RequestDaemonToBastionV1", nil
	case am.OpenDataChannel:
		return "OpenDataChannelDaemonToBastionV1", nil
	case am.CloseDataChannel:
		return "CloseDataChannelDaemonToBastionV1", nil
	default:
		return "", fmt.Errorf("unhandled message type: %s", agentMessage.MessageType)
	}
}

func parseFlags() error {
	flag.StringVar(&sessionId, "sessionId", "", "Session ID From Zli")
	flag.StringVar(&authHeader, "authHeader", "", "Auth Header From Zli")

	// Our expected flags we need to start
	flag.StringVar(&serviceUrl, "serviceURL", "", "Service URL to use")
	flag.StringVar(&targetUser, "targetUser", "", "Kube Role to Assume")
	flag.StringVar(&targetGroupsRaw, "targetGroups", "", "Kube Group to Assume")
	flag.StringVar(&targetClusterId, "targetClusterId", "", "Kube Cluster Id to Connect to")
	flag.StringVar(&environmentId, "environmentId", "", "Environment Id of cluster we are connecting too")

	// Plugin variables
	flag.StringVar(&localhostToken, "localhostToken", "", "Localhost Token to Validate Kubectl commands")
	flag.StringVar(&daemonPort, "daemonPort", "", "Daemon Port To Use")
	flag.StringVar(&certPath, "certPath", "", "Path to cert to use for our localhost server")
	flag.StringVar(&keyPath, "keyPath", "", "Path to key to use for our localhost server")
	flag.StringVar(&configPath, "configPath", "", "Local storage path to zli config")
	flag.StringVar(&logPath, "logPath", "", "Path to log file for daemon")
	flag.StringVar(&refreshTokenCommand, "refreshTokenCommand", "", "zli constructed command for refreshing id tokens")

	flag.Parse()

	// Check we have all required flags
	if sessionId == "" || authHeader == "" || targetUser == "" || targetGroupsRaw == "" || targetClusterId == "" || serviceUrl == "" ||
		daemonPort == "" || localhostToken == "" || environmentId == "" || certPath == "" || keyPath == "" ||
		logPath == "" || configPath == "" {
		return fmt.Errorf("missing flags")
	}

	// Parse target groups
	targetGroups = strings.Split(targetGroupsRaw, ",")

	return nil
}

func getLogFilePath() string {
	return logPath
}
