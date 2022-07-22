package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"bastionzero.com/bctl/v1/bctl/daemon/servers/dbserver"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/kubeserver"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/shellserver"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/sshserver"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/webserver"
	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/error/errorreport"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert/zliconfig"
	bzlogger "bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
)

const (
	daemonVersion  = "$DAEMON_VERSION"
	prodServiceUrl = "https://cloud.bastionzero.com"
)

type EnvVar struct {
	Value string
	Seen  bool
}

// environment variable management dictionary
// maps the name of an env var to its value and whether or not it is set
var (
	config = map[string]EnvVar{
		// general-purpose configuration
		"SESSION_ID":                    {},                               // Session id from Zli
		"SESSION_TOKEN":                 {},                               // Session token from Zli
		"AUTH_HEADER":                   {},                               // Auth header from Zli
		"LOG_LEVEL":                     {Value: bzlogger.Debug.String()}, // The log level to use
		"CONNECTION_ID":                 {},                               // The bzero connection id for the shell connection
		"CONNECTION_SERVICE_URL":        {},                               // URL of connection service
		"CONNECTION_SERVICE_AUTH_TOKEN": {},                               // The auth token returned from the universal connection service
		"SERVICE_URL":                   {Value: prodServiceUrl},          // URL of bastion
		"TARGET_ID":                     {},                               // Id of the target to connect to
		"PLUGIN":                        {},                               // Plugin to activate
		"AGENT_PUB_KEY":                 {},                               // Base64 encoded string of agent's public key

		// for interacting with the user and the ZLI
		"LOCAL_PORT":            {}, // Daemon port To Use
		"LOCAL_HOST":            {}, // Daemon host To Use
		"CONFIG_PATH":           {}, // Local storage path to zli config
		"LOG_PATH":              {}, // Path to log file for daemon
		"REFRESH_TOKEN_COMMAND": {}, // zli constructed command for refreshing id tokens

		// variables used by multiple plugins
		"REMOTE_PORT": {Value: "-1"}, // Remote target port to connect to
		"REMOTE_HOST": {},            // Remote target host to connect to

		"TARGET_USER": {}, // OS user or Kube role to assume

		// kube plugin variables
		"TARGET_GROUPS":   {}, // Comma-separated list of Kube groups to assume
		"LOCALHOST_TOKEN": {}, // token to validate kube commands
		"CERT_PATH":       {}, // Path to cert to use for our localhost server
		"KEY_PATH":        {}, // Path to key to use for our localhost server

		// shell plugin variables
		"DATACHANNEL_ID": {}, // The datachannel id to attach to an existing shell connection

		// ssh plugin variables
		"IDENTITY_FILE":    {}, // Path to an SSH IdentityFile
		"KNOWN_HOSTS_FILE": {}, // Path to bastionzero-known_hosts
		"SSH_ACTION":       {}, // One of ['opaque', 'transparent']
		"HOSTNAMES":        {}, // Comma-separated list of hostNames to use for this target
	}
)

func main() {
	envErr := loadEnvironment()

	if logger, err := createLogger(); err != nil {
		reportError(logger, err)
	} else {
		// print out loadEnvironment error now
		if envErr != nil {
			reportError(logger, envErr)
		} else {
			// Create our headers and params
			headers := make(map[string]string)
			headers["Authorization"] = config["AUTH_HEADER"].Value

			// Add our sessionId and token into the header
			headers["Cookie"] = fmt.Sprintf("sessionId=%s; sessionToken=%s", config["SESSION_ID"].Value, config["SESSION_TOKEN"].Value)

			params := make(map[string]string)
			params["version"] = daemonVersion

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

func createLogger() (*bzlogger.Logger, error) {
	options := &bzlogger.Config{
		FilePath: config["LOG_PATH"].Value,
	}

	// For the shell and ssh plugins we read/write directly from Stdin/Stdout so we dont want
	// our logs to show up there
	plugin := config["PLUGIN"].Value
	if plugin != string(bzplugin.Shell) && plugin != string(bzplugin.Ssh) {
		options.ConsoleWriters = []io.Writer{os.Stdout}
	}

	logger, err := bzlogger.New(options)
	logger.AddDaemonVersion(daemonVersion)
	return logger, err
}

func reportError(logger *bzlogger.Logger, errorReport error) {
	if logger != nil {
		logger.Error(errorReport)
	}

	errReport := errorreport.ErrorReport{
		Reporter:  "daemon-" + daemonVersion,
		Timestamp: fmt.Sprint(time.Now().Unix()),
		Message:   errorReport.Error(),
		State: map[string]string{
			"targetHostName": "",
			"goos":           runtime.GOOS,
			"goarch":         runtime.GOARCH,
		},
	}

	errorreport.ReportError(logger, config["SERVICE_URL"].Value, errReport)
}

func startServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string) error {
	connectionServiceUrl := config["CONNECTION_SERVICE_URL"].Value
	plugin := config["PLUGIN"].Value

	logger.Infof("Opening websocket to the Connection Node: %s for plugin %s", connectionServiceUrl, plugin)

	params["connection_id"] = config["CONNECTION_ID"].Value
	params["connectionServiceUrl"] = connectionServiceUrl
	params["connectionServiceAuthToken"] = config["CONNECTION_SERVICE_AUTH_TOKEN"].Value

	// create our MrZAP object
	zliConfig, err := zliconfig.New(config["CONFIG_PATH"].Value, config["REFRESH_TOKEN_COMMAND"].Value)
	if err != nil {
		return err
	}
	cert, err := bzcert.New(zliConfig)
	if err != nil {
		return err
	}

	switch bzplugin.PluginName(plugin) {
	case bzplugin.Db:
		params["websocketType"] = "db"
		return startDbServer(logger, headers, params, cert)
	case bzplugin.Kube:
		params["websocketType"] = "cluster"
		return startKubeServer(logger, headers, params, cert)
	case bzplugin.Shell:
		params["websocketType"] = "shell"
		return startShellServer(logger, headers, params, cert)
	case bzplugin.Ssh:
		params["websocketType"] = "ssh"
		return startSshServer(logger, headers, params, cert)
	case bzplugin.Web:
		params["websocketType"] = "web"
		return startWebServer(logger, headers, params, cert)
	default:
		return fmt.Errorf("unhandled plugin passed when trying to start server: %s", plugin)
	}
}

func startSshServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("sshserver")

	params["target_id"] = config["TARGET_ID"].Value
	params["target_user"] = config["TARGET_USER"].Value
	params["remote_host"] = config["REMOTE_HOST"].Value
	params["remote_port"] = config["REMOTE_PORT"].Value
	remotePort, err := strconv.Atoi(config["REMOTE_PORT"].Value)
	if err != nil {
		return fmt.Errorf("failed to parse remote port: %s", err)
	}

	return sshserver.StartSshServer(
		subLogger,
		config["TARGET_USER"].Value,
		config["DATACHANNEL_ID"].Value,
		cert,
		config["SERVICE_URL"].Value,
		params,
		headers,
		config["AGENT_PUB_KEY"].Value,
		targetSelectHandler,
		config["IDENTITY_FILE"].Value,
		config["KNOWN_HOSTS_FILE"].Value,
		strings.Split(config["HOSTNAMES"].Value, ","),
		config["REMOTE_HOST"].Value,
		remotePort,
		config["LOCAL_PORT"].Value,
		config["SSH_ACTION"].Value,
	)
}

func startShellServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("shellserver")

	return shellserver.StartShellServer(
		subLogger,
		config["TARGET_USER"].Value,
		config["DATACHANNEL_ID"].Value,
		cert,
		config["SERVICE_URL"].Value,
		params,
		headers,
		config["AGENT_PUB_KEY"].Value,
		targetSelectHandler,
	)
}

func startWebServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("webserver")

	params["target_id"] = config["TARGET_ID"].Value
	remotePort, err := strconv.Atoi(config["REMOTE_PORT"].Value)
	if err != nil {
		return fmt.Errorf("failed to parse remote port: %s", err)
	}

	return webserver.StartWebServer(
		subLogger,
		config["LOCAL_PORT"].Value,
		config["LOCAL_HOST"].Value,
		remotePort,
		config["REMOTE_HOST"].Value,
		cert,
		config["SERVICE_URL"].Value,
		params,
		headers,
		config["AGENT_PUB_KEY"].Value,
		targetSelectHandler)
}

func startDbServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("dbserver")

	params["target_id"] = config["TARGET_ID"].Value
	remotePort, err := strconv.Atoi(config["REMOTE_PORT"].Value)
	if err != nil {
		return fmt.Errorf("failed to parse remote port: %s", err)
	}

	return dbserver.StartDbServer(
		subLogger,
		config["LOCAL_PORT"].Value,
		config["LOCAL_HOST"].Value,
		remotePort,
		config["REMOTE_HOST"].Value,
		cert,
		config["SERVICE_URL"].Value,
		params,
		headers,
		config["AGENT_PUB_KEY"].Value,
		targetSelectHandler)
}

func startKubeServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("kubeserver")

	// Set our param value for target_user and target_group
	params["target_id"] = config["TARGET_ID"].Value
	params["target_user"] = config["TARGET_USER"].Value
	params["target_groups"] = config["TARGET_GROUPS"].Value

	var targetGroups []string
	if config["TARGET_GROUPS"].Value != "" {
		targetGroups = strings.Split(config["TARGET_GROUPS"].Value, ",")
	}

	return kubeserver.StartKubeServer(
		subLogger,
		config["LOCAL_PORT"].Value,
		config["LOCAL_HOST"].Value,
		config["CERT_PATH"].Value,
		config["KEY_PATH"].Value,
		cert,
		config["TARGET_USER"].Value,
		targetGroups,
		config["LOCALHOST_TOKEN"].Value,
		config["SERVICE_URL"].Value,
		params,
		headers,
		config["AGENT_PUB_KEY"].Value,
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

// read all environment variables and apply the processing for specific fields that need it
func loadEnvironment() error {
	for varName, entry := range config {
		entry.Value, entry.Seen = os.LookupEnv(varName)
		config[varName] = entry
	}

	// Make sure our service url is correctly formatted
	if err := formatServiceUrl(); err != nil {
		return err
	}

	// Check we have all required flags
	// Depending on the plugin ensure we have the correct required flag values
	requriedVars := []string{"CONNECTION_ID", "CONNECTION_SERVICE_URL", "CONNECTION_SERVICE_AUTH_TOKEN", "SESSION_ID", "SESSION_TOKEN", "AUTH_HEADER", "LOG_PATH", "CONFIG_PATH", "AGENT_PUB_KEY", "REFRESH_TOKEN_COMMAND"}
	plugin := config["PLUGIN"].Value
	switch bzplugin.PluginName(plugin) {
	case bzplugin.Kube:
		requriedVars = append(requriedVars, "LOCAL_PORT", "TARGET_USER", "TARGET_ID", "LOCALHOST_TOKEN", "CERT_PATH", "KEY_PATH")
	case bzplugin.Db:
		fallthrough
	case bzplugin.Web:
		requriedVars = append(requriedVars, "LOCAL_PORT", "REMOTE_HOST", "REMOTE_PORT")
	case bzplugin.Shell:
		requriedVars = append(requriedVars, "TARGET_USER", "CONNECTION_ID")
	case bzplugin.Ssh:
		requriedVars = append(requriedVars, "TARGET_USER", "TARGET_ID", "REMOTE_HOST", "REMOTE_PORT", "IDENTITY_FILE", "KNOWN_HOSTS_FILE", "HOSTNAMES", "SSH_ACTION")
	default:
		return fmt.Errorf("unhandled plugin passed: %s", plugin)
	}

	// Check against required dict to find the missing ones
	var missingVars []string
	for _, req := range requriedVars {
		if !config[req].Seen {
			missingVars = append(missingVars, req)
		}
	}

	if len(missingVars) > 0 {
		return fmt.Errorf("the following required environment variables are not set: %v", missingVars)
	}

	return nil
}

func formatServiceUrl() error {
	serviceUrlEntry := config["SERVICE_URL"]
	serviceUrl := serviceUrlEntry.Value
	if !strings.HasPrefix(serviceUrl, "http") {
		if url, err := bzhttp.BuildEndpoint("https://", serviceUrl); err != nil {
			return fmt.Errorf("error adding scheme to serviceUrl %s: %s", serviceUrl, err)
		} else {
			serviceUrlEntry.Value = url
			config["SERVICE_URL"] = serviceUrlEntry
		}
	}
	return nil
}
