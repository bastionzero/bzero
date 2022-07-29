package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"os/signal"
	"syscall"

	"github.com/google/uuid"

	"bastionzero.com/bctl/v1/bctl/agent/controlchannel"
	"bastionzero.com/bctl/v1/bctl/agent/rbac"
	"bastionzero.com/bctl/v1/bctl/agent/registration"
	"bastionzero.com/bctl/v1/bctl/agent/vault"
	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/error/errorreport"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

var (
	serviceUrl, orgId                string
	environmentId, environmentName   string
	activationToken, registrationKey string
	idpProvider, namespace, idpOrgId string
	targetId, targetName, agentType  string
	logLevel                         string
	forceReRegistration              bool
	wait                             bool
	printVersion                     bool
	listLogFile                      bool
)

const (
	Cluster = "cluster"
	Bzero   = "bzero"

	prodServiceUrl = "https://cloud.bastionzero.com/"

	// we replace "bzero-agent" at build with process name
	bzeroLogFilePath = "/var/log/bzero/bzero-agent.log"
)

func main() {
	setAgentType()

	parseErr := parseFlags()

	if logger, err := setupLogger(); err != nil {
		reportError(logger, err)
	} else if parseErr != nil {
		// catch our parser errors now that we have a logger to print them
		reportError(logger, err)
	} else if printVersion {
		fmt.Printf("%s\n", getAgentVersion())
		return
	} else if listLogFile {
		switch agentType {
		case Bzero:
			fmt.Printf("%s\n", bzeroLogFilePath)
		case Cluster:
			fmt.Printf("BZero Agent logs can be accessed via the Kube API server by tailing the pods logs\n")
		}
		return
	} else {

		logger.Infof("BastionZero Agent version %s starting up...", getAgentVersion())

		// Check if the agent is registered or not.  If not, generate signing keys,
		// check kube permissions and setup, and register with the Bastion.
		if err := handleRegistration(logger); err != nil {

			// our systemd agent waits for a successful new registration
			if wait {
				vault.WaitForNewRegistration(logger)
				logger.Infof("New registration detected. Loading registration information!")

				// double check and set our local variables
				if registered, err := isRegistered(); err != nil {
					logger.Error(err)
				} else if registered {
					run(logger)
				}
			}
		} else {
			run(logger)
		}
	}

	switch agentType {
	case Cluster:

		// Sleep forever because otherwise kube will endlessly try restarting
		// Ref: https://stackoverflow.com/questions/36419054/go-projects-main-goroutine-sleep-forever
		select {}
	case Bzero:
		os.Exit(1)
	}
}

func run(logger *logger.Logger) {
	defer func() {
		// recover in case the agent panics
		if msg := recover(); msg != nil {
			reportError(logger, fmt.Errorf("bzero agent crashed with panic: %+v", msg))
			os.Exit(1)
		}
	}()

	// Connect the control channel to BastionZero
	logger.Info("Creating connection to BastionZero...")
	if control, err := startControlChannel(logger, getAgentVersion()); err != nil {
		reportError(logger, err)
	} else {
		// wait until we recieve a kill signal and quit
		signal := blockUntilSignaled()
		control.Close(fmt.Errorf("got signal: %v value: %v", signal, signal.String()))
		os.Exit(1)
	}
}

func setupLogger() (*logger.Logger, error) {
	config := logger.Config{
		ConsoleWriters: []io.Writer{os.Stdout},
	}

	// if this is systemd, output log to file
	if agentType == Bzero {
		config.FilePath = bzeroLogFilePath
	}

	log, err := logger.New(&config)
	if err != nil {
		log.AddAgentVersion(getAgentVersion())
	}

	return log, err
}

// report early errors to the bastion so we have greater visibility
func reportError(logger *logger.Logger, errorReport error) {
	if logger != nil {
		logger.Error(errorReport)
	} else {
		fmt.Println(errorReport.Error())
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}

	errReport := errorreport.ErrorReport{
		Reporter:  "agent-" + getAgentVersion(),
		Timestamp: fmt.Sprint(time.Now().Unix()),
		Message:   errorReport.Error(),
		State: map[string]string{
			"activationToken":       activationToken,
			"registrationKeyLength": fmt.Sprintf("%v", len(registrationKey)),
			"targetName":            targetName,
			"targetHostName":        hostname,
			"goos":                  runtime.GOOS,
			"goarch":                runtime.GOARCH,
		},
	}

	errorreport.ReportError(logger, serviceUrl, errReport)
}

// ref: https://github.com/bastionzero/bzero-ssm-agent/blob/76d133c565bb7e11683f63fbc23d39fa0840df14/core/agent.go#L89
func blockUntilSignaled() os.Signal {
	// Below channel will handle all machine initiated shutdown/reboot requests.

	// Set up channel on which to receive signal notifications.
	// We must use a buffered channel or risk missing the signal
	// if we're not ready to receive when the signal is sent.
	c := make(chan os.Signal, 1)

	// Listening for OS signals is a blocking call.
	// Only listen to signals that require us to exit.
	// Otherwise we will continue execution and exit the program.
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	s := <-c
	return s
}

func startControlChannel(logger *logger.Logger, agentVersion string) (*controlchannel.ControlChannel, error) {
	// Load in our saved config
	config, err := vault.LoadVault()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve vault: %s", err)
	}

	// Create our headers and params, headers are empty
	headers := make(map[string]string)

	// Make and add our params
	params := map[string]string{
		"public_key": config.Data.PublicKey,
		"version":    agentVersion,
		"target_id":  config.Data.TargetId,
		"agent_type": agentType,
	}

	// create a websocket
	wsId := uuid.New().String()
	wsLogger := logger.GetWebsocketLogger(wsId) // TODO: replace with actual connectionId
	websocket, err := websocket.New(wsLogger, serviceUrl, params, headers, true, true, websocket.AgentControl)
	if err != nil {
		return nil, err
	}

	// create logger for control channel
	ccId := uuid.New().String()
	ccLogger := logger.GetControlChannelLogger(ccId)

	return controlchannel.Start(ccLogger, ccId, websocket, serviceUrl, agentType, config)
}

func parseFlags() error {
	// Helpful flags
	flag.BoolVar(&printVersion, "version", false, "Print current version of the agent")
	flag.BoolVar(&listLogFile, "logs", false, "Print the agent log file path")

	// Our required registration flags
	flag.StringVar(&activationToken, "activationToken", "", "Single-use token used to register the agent")
	flag.StringVar(&registrationKey, "registrationKey", "", "API Key used to register the agent")

	// forced re-registration flags
	flag.BoolVar(&forceReRegistration, "y", false, "Boolean flag if you want to force the agent to re-register")
	flag.BoolVar(&forceReRegistration, "f", false, "Same as -y")

	// Our flag to determine if this is systemd and will therefore wait for successful registration
	flag.BoolVar(&wait, "w", false, "Mode for background processes to wait for successful registration")

	// All optional flags
	flag.StringVar(&serviceUrl, "serviceUrl", prodServiceUrl, "Service URL to use")
	flag.StringVar(&orgId, "orgId", "", "OrgID to use")
	flag.StringVar(&targetName, "targetName", "", "Target name to use")
	flag.StringVar(&targetId, "targetId", "", "Target ID to use")
	flag.StringVar(&logLevel, "logLevel", logger.Debug.String(), "The log level to use")

	flag.StringVar(&environmentId, "environmentId", "", "Policy environment ID to associate with agent")
	flag.StringVar(&environmentName, "environmentName", "", "(Deprecated) Policy environment Name to associate with agent")

	// new env flags
	flag.StringVar(&environmentId, "envId", "", "(Deprecated) Please use environmentId")
	flag.StringVar(&environmentName, "envName", "", "(Deprecated) Policy environment Name to associate with agent")

	// Parse any flag
	flag.Parse()

	// The environment will overwrite any flags passed
	if agentType == Cluster {
		serviceUrl = os.Getenv("SERVICE_URL")
		activationToken = os.Getenv("ACTIVATION_TOKEN")
		targetName = os.Getenv("TARGET_NAME")
		targetId = os.Getenv("TARGET_ID")
		environmentId = os.Getenv("ENVIRONMENT")
		idpProvider = os.Getenv("IDP_PROVIDER")
		idpOrgId = os.Getenv("IDP_ORG_ID")
		namespace = os.Getenv("NAMESPACE")
		registrationKey = os.Getenv("API_KEY")
	}

	// Make sure our service url is correctly formatted
	if !strings.HasPrefix(serviceUrl, "http") {
		if url, err := bzhttp.BuildEndpoint("https://", serviceUrl); err != nil {
			return fmt.Errorf("error adding scheme to serviceUrl %s: %s", serviceUrl, err)
		} else {
			serviceUrl = url
		}
	}
	return nil
}

func handleRegistration(logger *logger.Logger) error {
	// Check if there is a public key in the vault, if not then agent is not registered
	if registered, err := isRegistered(); err != nil {
		logger.Error(err)
		return err
	} else if !registered && wait {
		logger.Info("Agent waiting for registration...")
		return fmt.Errorf("")
	} else if !registered || forceReRegistration {

		// Only check RBAC permissions if we are inside a cluster
		if vault.InCluster() {
			if err := rbac.CheckPermissions(logger, namespace); err != nil {
				rerr := fmt.Errorf("error verifying agent kubernetes setup: %s", err)
				logger.Error(rerr)
				return rerr
			} else {
				logger.Info("Namespace and service account permissions verified")
			}
		}

		// register the agent with bastion, if not already registered
		if err := registration.Register(logger, serviceUrl, activationToken, registrationKey, targetId); err != nil {
			reportError(logger, err)
			return err
		}

		os.Exit(0)
	} else {
		logger.Infof("Bzero Agent is already registered with %s", serviceUrl)
	}

	return nil
}

func isRegistered() (bool, error) {
	registered := false

	// load out config
	if config, err := vault.LoadVault(); err != nil {
		return registered, fmt.Errorf("could not load vault: %s", err)
	} else if (config.Data.PublicKey == "" || forceReRegistration) && flag.NFlag() > 0 { // no public key means unregistered
		if !wait {

			// we need either an activation token or an registration key to register the agent
			if activationToken == "" && registrationKey == "" {
				return registered, fmt.Errorf("in order to register the agent, user must provide either an activation token or api key")
			}

			// Save flags passed to our config so registration can access them
			config.Data = vault.SecretData{
				ServiceUrl:      serviceUrl,
				Namespace:       namespace,
				IdpProvider:     idpProvider,
				IdpOrgId:        idpOrgId,
				EnvironmentId:   environmentId,
				EnvironmentName: environmentName,
				AgentType:       agentType,
				TargetName:      targetName,
				Version:         getAgentVersion(),
			}
			if err := config.Save(); err != nil {
				return registered, fmt.Errorf("error saving vault: %s", err)
			}
		}
	} else {
		registered = true

		// load any variables we might need
		serviceUrl = config.Data.ServiceUrl
		targetName = config.Data.TargetName
	}

	return registered, nil
}

func getAgentVersion() string {
	if os.Getenv("DEV") == "true" {
		return "1.0"
	} else {
		return "$AGENT_VERSION"
	}
}

func setAgentType() {
	// determine agent type
	if vault.InCluster() {
		agentType = Cluster
	} else {
		agentType = Bzero
	}
}
