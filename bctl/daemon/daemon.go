package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
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

// Declaring flags as package-accessible variables
var (
	sessionId, authHeader, targetId, serviceUrl, plugin, logLevel    string
	sessionToken, logPath, refreshTokenCommand, localPort, localHost string
	agentPubKey                                                      string

	// Common/shared plugin arguments
	targetUser                 string
	remotePort                 int
	connectionServiceUrl       string
	connectionServiceAuthToken string

	// Kube server specifc arguments
	targetGroupsRaw, certPath, keyPath string
	localhostToken, configPath         string
	targetGroups                       []string

	// Db, web, and ssh specifc arguments
	remoteHost string

	// Shell specific arguments
	connectionId  string
	dataChannelId string

	// SSH specific arguments
	identityFile   string
	knownHostsFile string
	sshAction      string
	hostNames      string // comma-separated list
)

const (
	daemonVersion  = "$DAEMON_VERSION"
	prodServiceUrl = "https://cloud.bastionzero.com"
)

func main() {
	flagErr := parseFlags()

	if logger, err := createLogger(); err != nil {
		reportError(logger, err)
	} else {
		// print out parseflags error now
		if flagErr != nil {
			reportError(logger, flagErr)
		} else {
			// Create our headers and params
			headers := make(map[string]string)
			headers["Authorization"] = authHeader

			// Add our sessionId and token into the header
			headers["Cookie"] = fmt.Sprintf("sessionId=%s; sessionToken=%s", sessionId, sessionToken)

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
		FilePath: logPath,
	}

	// For shell plugin we read/write directly from Stdin/Stdout so we dont want
	// our logs to show up there
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

	errorreport.ReportError(logger, serviceUrl, errReport)
}

func startServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string) error {
	logger.Infof("Opening websocket to the Connection Node: %s for plugin %s", connectionServiceUrl, plugin)

	params["connection_id"] = connectionId
	params["connectionServiceUrl"] = connectionServiceUrl
	params["connectionServiceAuthToken"] = connectionServiceAuthToken

	// create our MrZAP object
	config, err := zliconfig.New(configPath, refreshTokenCommand)
	if err != nil {
		return err
	}
	cert, err := bzcert.New(config)
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

	params["target_id"] = targetId
	params["target_user"] = targetUser
	params["remote_host"] = remoteHost
	params["remote_port"] = fmt.Sprintf("%d", remotePort)

	return sshserver.StartSshServer(
		subLogger,
		targetUser,
		dataChannelId,
		cert,
		serviceUrl,
		params,
		headers,
		agentPubKey,
		targetSelectHandler,
		identityFile,
		knownHostsFile,
		strings.Split(hostNames, ","),
		remoteHost,
		remotePort,
		localPort,
		sshAction,
	)
}

func startShellServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("shellserver")

	return shellserver.StartShellServer(
		subLogger,
		targetUser,
		dataChannelId,
		cert,
		serviceUrl,
		params,
		headers,
		agentPubKey,
		targetSelectHandler,
	)
}

func startWebServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("webserver")

	params["target_id"] = targetId

	return webserver.StartWebServer(subLogger,
		localPort,
		localHost,
		remotePort,
		remoteHost,
		cert,
		serviceUrl,
		params,
		headers,
		agentPubKey,
		targetSelectHandler)
}

func startDbServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("dbserver")

	params["target_id"] = targetId

	return dbserver.StartDbServer(subLogger,
		localPort,
		localHost,
		remotePort,
		remoteHost,
		cert,
		serviceUrl,
		params,
		headers,
		agentPubKey,
		targetSelectHandler)
}

func startKubeServer(logger *bzlogger.Logger, headers map[string]string, params map[string]string, cert *bzcert.BZCert) error {
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
		cert,
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
	flag.StringVar(&sessionToken, "sessionToken", "", "Session Token From Zli")
	flag.StringVar(&authHeader, "authHeader", "", "Auth Header From Zli")
	flag.StringVar(&logLevel, "logLevel", bzlogger.Debug.String(), "The log level to use")
	flag.StringVar(&connectionId, "connectionId", "", "The bzero connection id for the shell connection")
	flag.StringVar(&connectionServiceUrl, "connectionServiceUrl", "", "The bzero connection id for the shell connection")
	flag.StringVar(&connectionServiceAuthToken, "connectionServiceAuthToken", "", "The bzero connection id for the shell connection")

	// Our expected flags we need to start
	flag.StringVar(&serviceUrl, "serviceURL", prodServiceUrl, "Service URL to use")
	flag.StringVar(&targetId, "targetId", "", "Kube Cluster Id to Connect to")
	flag.StringVar(&plugin, "plugin", "", "Plugin to activate (kube, db, web)")
	flag.StringVar(&localPort, "localPort", "", "Daemon Port To Use")
	flag.StringVar(&localHost, "localHost", "", "Daemon Post To Use")
	flag.StringVar(&agentPubKey, "agentPubKey", "", "Base64 encoded string of agent's public key")

	// Kube plugin variables
	flag.StringVar(&targetGroupsRaw, "targetGroups", "", "Kube Group to Assume")
	flag.StringVar(&targetUser, "targetUser", "", "Kube Role or OS user to Assume")
	flag.StringVar(&localhostToken, "localhostToken", "", "Localhost Token to Validate Kubectl commands")
	flag.StringVar(&certPath, "certPath", "", "Path to cert to use for our localhost server")
	flag.StringVar(&keyPath, "keyPath", "", "Path to key to use for our localhost server")
	flag.StringVar(&configPath, "configPath", "", "Local storage path to zli config")
	flag.StringVar(&logPath, "logPath", "", "Path to log file for daemon")
	flag.StringVar(&refreshTokenCommand, "refreshTokenCommand", "", "zli constructed command for refreshing id tokens")

	// Db/Web plugin variables
	flag.IntVar(&remotePort, "remotePort", -1, "Remote target port to connect to")
	flag.StringVar(&remoteHost, "remoteHost", "", "Remote target host to connect to")

	// Shell plugin variables
	flag.StringVar(&dataChannelId, "dataChannelId", "", "The datachannel id to attach to an existing shell connection")

	// SSH plugin variables
	flag.StringVar(&identityFile, "identityFile", "", "Path to an SSH IdentityFile")
	flag.StringVar(&knownHostsFile, "knownHostsFile", "", "Path to bz-known_hosts")
	flag.StringVar(&sshAction, "sshAction", "", "One of ['opaque', 'transparent']")
	flag.StringVar(&hostNames, "hostNames", "", "Comma-separated list of hostNames to use for this target")

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
	requiredFlags := []string{"connectionId", "connectionServiceUrl", "connectionServiceAuthToken", "sessionId", "sessionToken", "authHeader", "logPath", "configPath", "agentPubKey"}
	switch bzplugin.PluginName(plugin) {
	case bzplugin.Kube:
		requiredFlags = append(requiredFlags, "localPort", "targetUser", "targetId", "localhostToken", "certPath", "keyPath")
	case bzplugin.Db:
		fallthrough
	case bzplugin.Web:
		requiredFlags = append(requiredFlags, "localPort", "remoteHost", "remotePort")
	case bzplugin.Shell:
		requiredFlags = append(requiredFlags, "targetUser", "connectionId")
	case bzplugin.Ssh:
		requiredFlags = append(requiredFlags, "targetUser", "targetId", "remoteHost", "identityFile", "knownHostsFile", "hostNames", "remotePort", "sshAction")
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
