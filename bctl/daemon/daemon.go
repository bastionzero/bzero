package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"bastionzero.com/bctl/v1/bctl/daemon/exitcodes"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/dbserver"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/kubeserver"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/shellserver"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/sshserver"
	"bastionzero.com/bctl/v1/bctl/daemon/servers/webserver"
	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/error/errorreport"

	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert/zliconfig"
	bzlogger "bastionzero.com/bctl/v1/bzerolib/logger"
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
)

const (
	daemonVersion  = "$DAEMON_VERSION"
	prodServiceUrl = "https://cloud.bastionzero.com"
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
			headers["Authorization"] = config[AUTH_HEADER].Value

			// Add our sessionId and token into the header
			headers["Cookie"] = fmt.Sprintf("sessionId=%s; sessionToken=%s", config[SESSION_ID].Value, config[SESSION_TOKEN].Value)

			params := make(map[string]string)
			params["version"] = daemonVersion

			if err := startServer(logger, headers, params); err != nil {
				logger.Error(err)
				os.Exit(exitcodes.UNSPECIFIED_ERROR)
			} else {
				select {} // sleep forever
			}
		}
	}

	// if we hit this, something has gone wrong
	os.Exit(exitcodes.UNSPECIFIED_ERROR)
}

func createLogger() (*bzlogger.Logger, error) {
	options := &bzlogger.Config{
		FilePath: config[LOG_PATH].Value,
	}

	// For the shell and ssh plugins we read/write directly from Stdin/Stdout so we dont want
	// our logs to show up there
	plugin := config[PLUGIN].Value
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

	errorreport.ReportError(logger, config[SERVICE_URL].Value, errReport)
}

func startServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string) error {
	connectionServiceUrl := config[CONNECTION_SERVICE_URL].Value
	plugin := config[PLUGIN].Value

	logger.Infof("Opening websocket to the Connection Node: %s for plugin %s", connectionServiceUrl, plugin)

	params["connection_id"] = config[CONNECTION_ID].Value
	params["connectionServiceUrl"] = connectionServiceUrl
	params["connectionServiceAuthToken"] = config[CONNECTION_SERVICE_AUTH_TOKEN].Value

	// create our MrZAP object
	zliConfig, err := zliconfig.New(config[CONFIG_PATH].Value, config[REFRESH_TOKEN_COMMAND].Value)
	if err != nil {
		return err
	}

	// This validates the bzcert before creating the server so we can fail
	// fast if the cert is no longer valid. This may result in prompting the
	// user to login again if the cert contains expired IdP id tokens
	cert, err := bzcert.New(zliConfig)
	if err != nil {
		exitcodes.HandleDaemonError(err, logger)

		logger.Errorf("unknown error verifying bbzcert: %s", err)
		os.Exit(exitcodes.UNSPECIFIED_ERROR)
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

func startSshServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.DaemonBZCert) error {
	subLogger := logger.GetComponentLogger("sshserver")

	params["target_id"] = config[TARGET_ID].Value
	params["target_user"] = config[TARGET_USER].Value
	params["remote_host"] = config[REMOTE_HOST].Value
	params["remote_port"] = config[REMOTE_PORT].Value
	remotePort, err := strconv.Atoi(config[REMOTE_PORT].Value)
	if err != nil {
		return fmt.Errorf("failed to parse remote port: %s", err)
	}

	return sshserver.StartSshServer(
		subLogger,
		config[TARGET_USER].Value,
		config[DATACHANNEL_ID].Value,
		cert,
		config[SERVICE_URL].Value,
		params,
		headers,
		config[AGENT_PUB_KEY].Value,
		targetSelectHandler,
		config[IDENTITY_FILE].Value,
		config[KNOWN_HOSTS_FILE].Value,
		strings.Split(config[HOSTNAMES].Value, ","),
		config[REMOTE_HOST].Value,
		remotePort,
		config[LOCAL_PORT].Value,
		config[SSH_ACTION].Value,
	)
}

func startShellServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.DaemonBZCert) error {
	subLogger := logger.GetComponentLogger("shellserver")

	return shellserver.StartShellServer(
		subLogger,
		config[TARGET_USER].Value,
		config[DATACHANNEL_ID].Value,
		cert,
		config[SERVICE_URL].Value,
		params,
		headers,
		config[AGENT_PUB_KEY].Value,
		targetSelectHandler,
	)
}

func startWebServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.DaemonBZCert) error {
	subLogger := logger.GetComponentLogger("webserver")

	params["target_id"] = config[TARGET_ID].Value
	remotePort, err := strconv.Atoi(config[REMOTE_PORT].Value)
	if err != nil {
		return fmt.Errorf("failed to parse remote port: %s", err)
	}

	return webserver.StartWebServer(
		subLogger,
		config[LOCAL_PORT].Value,
		config[LOCAL_HOST].Value,
		remotePort,
		config[REMOTE_HOST].Value,
		cert,
		config[SERVICE_URL].Value,
		params,
		headers,
		config[AGENT_PUB_KEY].Value,
		targetSelectHandler)
}

func startDbServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.DaemonBZCert) error {
	subLogger := logger.GetComponentLogger("dbserver")

	params["target_id"] = config[TARGET_ID].Value
	remotePort, err := strconv.Atoi(config[REMOTE_PORT].Value)
	if err != nil {
		return fmt.Errorf("failed to parse remote port: %s", err)
	}

	return dbserver.StartDbServer(
		subLogger,
		config[LOCAL_PORT].Value,
		config[LOCAL_HOST].Value,
		remotePort,
		config[REMOTE_HOST].Value,
		cert,
		config[SERVICE_URL].Value,
		params,
		headers,
		config[AGENT_PUB_KEY].Value,
		targetSelectHandler)
}

func startKubeServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.DaemonBZCert) error {
	subLogger := logger.GetComponentLogger("kubeserver")

	// Set our param value for target_user and target_group
	params["target_id"] = config[TARGET_ID].Value
	params["target_user"] = config[TARGET_USER].Value
	params["target_groups"] = config[TARGET_GROUPS].Value

	targetGroups := []string{}
	if config[TARGET_GROUPS].Value != "" {
		targetGroups = strings.Split(config[TARGET_GROUPS].Value, ",")
	}

	return kubeserver.StartKubeServer(
		subLogger,
		config[LOCAL_PORT].Value,
		config[LOCAL_HOST].Value,
		config[CERT_PATH].Value,
		config[KEY_PATH].Value,
		cert,
		config[TARGET_USER].Value,
		targetGroups,
		config[LOCALHOST_TOKEN].Value,
		config[SERVICE_URL].Value,
		params,
		headers,
		config[AGENT_PUB_KEY].Value,
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
	var requriedVars []string
	plugin := config[PLUGIN].Value
	if pluginVars, ok := requriedPluginVars[bzplugin.PluginName(plugin)]; !ok {
		return fmt.Errorf("unhandled plugin passed: %s", plugin)
	} else {
		requriedVars = append(requriedGlobalVars, pluginVars...)
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
	serviceUrlEntry := config[SERVICE_URL]
	serviceUrl := serviceUrlEntry.Value
	if !strings.HasPrefix(serviceUrl, "http") {
		if url, err := bzhttp.BuildEndpoint("https://", serviceUrl); err != nil {
			return fmt.Errorf("error adding scheme to serviceUrl %s: %s", serviceUrl, err)
		} else {
			serviceUrlEntry.Value = url
			config[SERVICE_URL] = serviceUrlEntry
		}
	}
	return nil
}
