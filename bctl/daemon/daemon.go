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
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
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
	identityFile string
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
			if err := startServer(logger); err != nil {
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

func startServer(logger *bzlogger.Logger) error {
	logger.Infof("Opening websocket to the Connection Node: %s for plugin %s", connectionServiceUrl, plugin)

	// create our MrZAP object
	config, err := zliconfig.New(configPath, refreshTokenCommand)
	if err != nil {
		return err
	}
	cert, err := bzcert.New(config)
	if err != nil {
		return err
	}

	// Create our headers, these are shared by everyone
	headers := map[string][]string{
		"Authorization": {authHeader},
		"Cookie":        {fmt.Sprintf("sessionId=%s", sessionId), fmt.Sprintf("sessionToken=%s", sessionToken)},
	}

	switch bzplugin.PluginName(plugin) {
	case bzplugin.Db:
		return startDbServer(logger, headers, cert)
	case bzplugin.Kube:
		return startKubeServer(logger, headers, cert)
	case bzplugin.Shell:
		return startShellServer(logger, headers, cert)
	case bzplugin.Ssh:
		return startSshServer(logger, headers, cert)
	case bzplugin.Web:
		return startWebServer(logger, headers, cert)
	default:
		return fmt.Errorf("unhandled plugin passed when trying to start server: %s", plugin)
	}
}

func startSshServer(logger *bzlogger.Logger, headers map[string][]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("sshserver")

	params := map[string][]string{
		"connectionId":   {connectionId},
		"authToken":      {connectionServiceAuthToken},
		"connectionType": {string(websocket.SSH)},

		"target_id":   {targetId},
		"target_user": {targetUser},
		"remote_host": {remoteHost},
		"remote_port": {fmt.Sprint(remotePort)},
	}

	return sshserver.StartSshServer(
		subLogger,
		targetUser,
		dataChannelId,
		cert,
		serviceUrl,
		connectionServiceUrl,
		params,
		headers,
		agentPubKey,
		identityFile,
		remoteHost,
		remotePort,
	)
}

func startShellServer(logger *bzlogger.Logger, headers map[string][]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("shellserver")

	params := map[string][]string{
		"connectionId":   {connectionId},
		"authToken":      {connectionServiceAuthToken},
		"connectionType": {string(websocket.SHELL)},
	}

	return shellserver.StartShellServer(
		subLogger,
		targetUser,
		dataChannelId,
		cert,
		serviceUrl,
		connectionServiceUrl,
		params,
		headers,
		agentPubKey,
	)
}

func startWebServer(logger *bzlogger.Logger, headers map[string][]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("webserver")

	params := map[string][]string{
		"connectionId":   {connectionId},
		"authToken":      {connectionServiceAuthToken},
		"connectionType": {string(websocket.WEB)},

		"target_id": {targetId},
	}

	return webserver.StartWebServer(subLogger,
		localPort,
		localHost,
		remotePort,
		remoteHost,
		cert,
		serviceUrl,
		connectionServiceUrl,
		params,
		headers,
		agentPubKey,
	)
}

func startDbServer(logger *bzlogger.Logger, headers map[string][]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("dbserver")

	params := map[string][]string{
		"connectionId":   {connectionId},
		"authToken":      {connectionServiceAuthToken},
		"connectionType": {string(websocket.DB)},

		"target_id": {targetId},
	}

	return dbserver.StartDbServer(subLogger,
		localPort,
		localHost,
		remotePort,
		remoteHost,
		cert,
		serviceUrl,
		connectionServiceUrl,
		params,
		headers,
		agentPubKey,
	)
}

func startKubeServer(logger *bzlogger.Logger, headers map[string][]string, cert *bzcert.BZCert) error {
	subLogger := logger.GetComponentLogger("kubeserver")

	// Set our param value for target_user and target_group
	params := map[string][]string{
		"connectionId":   {connectionId},
		"authToken":      {connectionServiceAuthToken},
		"connectionType": {string(websocket.KUBE)},

		"target_id":     {targetId},
		"target_user":   {targetUser},
		"target_groups": {targetGroupsRaw},
	}

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
		connectionServiceUrl,
		params,
		headers,
		agentPubKey,
	)
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
		requiredFlags = append(requiredFlags, "targetUser", "targetId", "identityFile", "remoteHost", "remotePort")
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
