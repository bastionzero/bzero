package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"bastionzero.com/bctl/v1/bctl/daemon/servers/dbserver"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/kubeserver"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/webserver"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

// Declaring flags as package-accessible variables
var (
	sessionId, authHeader, targetId, serviceUrl, plugin string
	logPath, refreshTokenCommand, daemonPort            string

	// Kube server specifc values
	targetGroupsRaw, targetUser, certPath, keyPath string
	localhostToken, configPath                     string
	targetGroups                                   []string

	// Db and web specifc values
	remoteHost string
	remotePort int
)

const (
	version = "$DAEMON_VERSION"
)

func main() {
	parseErr := parseFlags() // TODO: Output missing args error
	if parseErr != nil {
		// TODO: We should alert zli somehow?, in all of these panics in this file?
		panic(parseErr)
	}

	// Setup our loggers
	// TODO: Pass in debug level as flag or put it in the config
	logger, err := logger.New(logger.Debug, getLogFilePath())
	if err != nil {
		os.Exit(1)
	}
	logger.AddDaemonVersion(version)

	// Create our headers and params
	headers := make(map[string]string)
	headers["Authorization"] = authHeader

	params := make(map[string]string)
	params["session_id"] = sessionId
	params["version"] = version

	logger.Infof("Opening websocket to Bastion: %s for plugin %s", serviceUrl, plugin)

	switch plugin {
	case "kube":
		params["websocketType"] = "kube"
		if err := startKubeServer(logger, headers, params); err != nil {
			logger.Error(err)
			panic(err)
		}
	case "db":
		params["websocketType"] = "db"
		if err := startDbServer(logger, headers, params); err != nil {
			logger.Error(err)
			panic(err)
		}
	case "web":
		params["websocketType"] = "web"
		if err := startWebServer(logger, headers, params); err != nil {
			logger.Error(err)
			panic(err)
		}
	default:
		pluginErr := fmt.Errorf("unhandled plugin passed when trying to start server: %s", plugin)
		logger.Error(pluginErr)
		panic(pluginErr)
	}

	select {} // sleep forever?
}

func startWebServer(logger *logger.Logger, headers map[string]string, params map[string]string) error {
	subLogger := logger.GetComponentLogger("webserver")

	params["target_id"] = targetId

	return webserver.StartWebServer(subLogger,
		daemonPort,
		remotePort,
		remoteHost,
		refreshTokenCommand,
		configPath,
		serviceUrl,
		params,
		headers,
		targetSelectHandler)
}

func startDbServer(logger *logger.Logger, headers map[string]string, params map[string]string) error {
	subLogger := logger.GetComponentLogger("dbserver")

	params["target_id"] = targetId

	return dbserver.StartDbServer(subLogger,
		daemonPort,
		remotePort,
		remoteHost,
		refreshTokenCommand,
		configPath,
		serviceUrl,
		params,
		headers,
		targetSelectHandler)
}

func startKubeServer(logger *logger.Logger, headers map[string]string, params map[string]string) error {
	subLogger := logger.GetComponentLogger("kubeserver")

	// Set our param value for target_user and target_group
	params["target_id"] = targetId
	params["target_user"] = targetUser
	params["target_groups"] = targetGroupsRaw

	return kubeserver.StartKubeServer(subLogger,
		daemonPort,
		certPath,
		keyPath,
		refreshTokenCommand,
		configPath,
		targetUser,
		targetGroups,
		localhostToken,
		serviceUrl,
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
	flag.StringVar(&targetId, "targetId", "", "Kube Cluster Id to Connect to")
	flag.StringVar(&plugin, "plugin", "", "Plugin to activate (kube, db, web)")
	flag.StringVar(&daemonPort, "daemonPort", "", "Daemon Port To Use")

	// Kube plugin variables
	flag.StringVar(&targetGroupsRaw, "targetGroups", "", "Kube Group to Assume")
	flag.StringVar(&targetUser, "targetUser", "", "Kube Role to Assume")
	flag.StringVar(&localhostToken, "localhostToken", "", "Localhost Token to Validate Kubectl commands")
	flag.StringVar(&certPath, "certPath", "", "Path to cert to use for our localhost server")
	flag.StringVar(&keyPath, "keyPath", "", "Path to key to use for our localhost server")
	flag.StringVar(&configPath, "configPath", "", "Local storage path to zli config")
	flag.StringVar(&logPath, "logPath", "", "Path to log file for daemon")
	flag.StringVar(&refreshTokenCommand, "refreshTokenCommand", "", "zli constructed command for refreshing id tokens")

	// Db/Web plugin variables
	flag.IntVar(&remotePort, "remotePort", -1, "Remote target port to connect to")
	flag.StringVar(&remoteHost, "remoteHost", "", "Remote target host to connect to")

	// Check we have all required flags
	flag.Parse()
	if sessionId == "" || authHeader == "" || serviceUrl == "" ||
		logPath == "" || configPath == "" || daemonPort == "" {
		return fmt.Errorf("missing flags")
	}

	// Depending on the plugin ensure we have the correct values
	switch plugin {
	case "kube":
		if targetUser == "" || targetId == "" ||
			localhostToken == "" || certPath == "" || keyPath == "" {
			return fmt.Errorf("missing kube plugin flags")
		}
	case "db":
	case "web":
		// We need remotePort AND remoteHost
		if remotePort == -1 && remoteHost == "" {
			return fmt.Errorf("missing db/web plugin flags")
		}
	default:
		return fmt.Errorf("unhandled plugin passed: %s", plugin)
	}

	// Parse target groups
	targetGroups = []string{}
	if targetGroupsRaw != "" {
		targetGroups = strings.Split(targetGroupsRaw, ",")
	}

	return nil
}

func getLogFilePath() string {
	return logPath
}
