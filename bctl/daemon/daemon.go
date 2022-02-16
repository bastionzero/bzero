package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"bastionzero.com/bctl/v1/bctl/daemon/servers/dbserver"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/kubeserver"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/webserver"
	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/error/errorreport"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

// Declaring flags as package-accessible variables
var (
	sessionId, authHeader, targetId, serviceUrl, plugin             string
	logPath, refreshTokenCommand, localPort, localHost, agentPubKey string

	// Kube server specifc values
	targetGroupsRaw, targetUser, certPath, keyPath string
	localhostToken, configPath                     string
	targetGroups                                   []string

	// Db and web specifc values
	remoteHost string
	remotePort int
)

const (
	version        = "$DAEMON_VERSION"
	prodServiceUrl = "https://cloud.bastionzero.com"
)

func main() {
	flagErr := parseFlags()

	// Setup our loggers
	// TODO: Pass in debug level as flag or put it in the config
	if logger, err := logger.New(logger.Debug, logPath); err != nil {
		reportError(logger, err)
	} else {
		logger.AddDaemonVersion(version)

		// print out parseflags error now
		if flagErr != nil {
			reportError(logger, flagErr)
		} else {
			// Create our headers and params
			headers := make(map[string]string)
			headers["Authorization"] = authHeader

			params := make(map[string]string)
			params["session_id"] = sessionId
			params["version"] = version

			if err := startServer(logger, headers, params); err != nil {
				logger.Error(err)
				os.Exit(1)
			} else {
				select {} // sleep forever
			}
		}
	}

	// if we hit this, something has gone wrong
	os.Exit(1)
}

func reportError(logger *logger.Logger, errorReport error) {
	if logger != nil {
		logger.Error(errorReport)
	}

	errReport := errorreport.ErrorReport{
		Reporter:  "daemon-" + version,
		Timestamp: fmt.Sprint(time.Now().Unix()),
		Message:   errorReport.Error(),
		State: map[string]string{
			"targetHostName": "",
			"goos":           runtime.GOOS,
			"goarch":         runtime.GOARCH,
		},
	}

	errorreport.ReportError(logger, serviceUrl, errReport)
}

func startServer(logger *logger.Logger, headers map[string]string, params map[string]string) error {
	logger.Infof("Opening websocket to Bastion: %s for plugin %s", serviceUrl, plugin)

	switch plugin {
	case "kube":
		params["websocketType"] = "cluster"
		return startKubeServer(logger, headers, params)
	case "db":
		params["websocketType"] = "db"
		return startDbServer(logger, headers, params)
	case "web":
		params["websocketType"] = "web"
		return startWebServer(logger, headers, params)
	default:
		return fmt.Errorf("unhandled plugin passed when trying to start server: %s", plugin)
	}
}

func startWebServer(logger *logger.Logger, headers map[string]string, params map[string]string) error {
	subLogger := logger.GetComponentLogger("webserver")

	params["target_id"] = targetId

	return webserver.StartWebServer(subLogger,
		localPort,
		localHost,
		remotePort,
		remoteHost,
		refreshTokenCommand,
		configPath,
		serviceUrl,
		params,
		headers,
		agentPubKey,
		targetSelectHandler)
}

func startDbServer(logger *logger.Logger, headers map[string]string, params map[string]string) error {
	subLogger := logger.GetComponentLogger("dbserver")

	params["target_id"] = targetId

	return dbserver.StartDbServer(subLogger,
		localPort,
		localHost,
		remotePort,
		remoteHost,
		refreshTokenCommand,
		configPath,
		serviceUrl,
		params,
		headers,
		agentPubKey,
		targetSelectHandler)
}

func startKubeServer(logger *logger.Logger, headers map[string]string, params map[string]string) error {
	subLogger := logger.GetComponentLogger("kubeserver")

	// Set our param value for target_user and target_group
	params["target_id"] = targetId
	params["target_user"] = targetUser
	params["target_groups"] = targetGroupsRaw

	return kubeserver.StartKubeServer(subLogger,
		localPort,
		localHost,
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
		agentPubKey,
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
	flag.StringVar(&serviceUrl, "serviceURL", prodServiceUrl, "Service URL to use")
	flag.StringVar(&targetId, "targetId", "", "Kube Cluster Id to Connect to")
	flag.StringVar(&plugin, "plugin", "", "Plugin to activate (kube, db, web)")
	flag.StringVar(&localPort, "localPort", "", "Daemon Port To Use")
	flag.StringVar(&localHost, "localHost", "", "Daemon Post To Use")
	flag.StringVar(&agentPubKey, "agentPubKey", "", "Base64 encoded string of agent's public key")

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

	flag.Parse()

	// Make sure our service url is correctly formatted
	if !strings.HasPrefix(serviceUrl, "http") {
		if url, err := bzhttp.BuildEndpoint("https://", serviceUrl); err != nil {
			return fmt.Errorf("error adding scheme to serviceUrl %s: %s", serviceUrl, err)
		} else {
			serviceUrl = url
		}
	}

	// Check we have all required flags
	// Depending on the plugin ensure we have the correct required flag values
	requiredFlags := []string{"sessionId", "authHeader", "logPath", "configPath", "localPort", "agentPubKey"}
	switch plugin {
	case "kube":
		requiredFlags = append(requiredFlags, "targetUser", "targetId", "localhostToken", "certPath", "keyPath")
	case "db":
	case "web":
		requiredFlags = append(requiredFlags, "remoteHost", "remotePort")
	default:
		return fmt.Errorf("unhandled plugin passed: %s", plugin)
	}

	// Put all of the flags we've seen into a dict
	seen := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { seen[f.Name] = true })

	// Check against required dict to find the missing ones
	var missingFlags []string
	for _, req := range requiredFlags {
		if !seen[req] {
			missingFlags = append(missingFlags, req)
		}
	}

	if len(missingFlags) > 0 {
		return fmt.Errorf("missing flags! %v", missingFlags)
	}

	// Parse target groups
	targetGroups = []string{}
	if targetGroupsRaw != "" {
		targetGroups = strings.Split(targetGroupsRaw, ",")
	}

	return nil
}
