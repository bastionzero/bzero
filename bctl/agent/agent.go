package main

import (
	"flag"
	"fmt"
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
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/error/errorreport"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/utils"
)

var (
	serviceUrl, orgId                string
	environmentId, environmentName   string
	activationToken, apiKey          string
	idpProvider, namespace, idpOrgId string
	targetId, targetName, agentType  string
)

const (
	Cluster = "cluster"
	Bzero   = "bzero"

	prodServiceUrl = "https://cloud.bastionzero.com/"
)

func main() {
	parseErr := parseFlags()

	if logger, err := setupLogger(); err != nil {
		reportError(logger, err)
	} else if parseErr != nil {
		// catch our parser errors now that we have a logger to print them
		reportError(logger, parseErr)
	} else {
		logger.Infof("BastionZero Agent version %s starting up...", getAgentVersion())

		// Check if the agent is registered or not.  If not, generate signing keys,
		// check kube permissions and setup, and register with the Bastion.
		if err := handleRegistration(logger); err != nil {
			reportError(logger, err)
		} else {

			// Connect the control channel to BastionZero
			logger.Info("Creating connection to BastionZero...")
			if control, err := startControlChannel(logger, getAgentVersion()); err != nil {
				reportError(logger, err)
			} else {
				logger.Info("Connection created successfully. Listening for incoming commands...")

				if agentType == Bzero {
					signal := blockUntilSignaled()
					control.Close(fmt.Errorf("got signal: %v value: %v", signal, signal.String()))
				}
			}
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

func setupLogger() (*logger.Logger, error) {
	// setup our loggers
	if logger, err := logger.New(logger.Debug, ""); err != nil {
		return logger, err
	} else {
		logger.AddAgentVersion(getAgentVersion())
		return logger, nil
	}
}

// report early errors to the bastion so we have greater visibility
func reportError(logger *logger.Logger, errorReport error) {
	if logger != nil {
		logger.Error(errorReport)
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
			"activationToken": activationToken,
			"registrationKey": apiKey,
			"targetName":      targetName,
			"targetHostName":  hostname,
			"goos":            runtime.GOOS,
			"goarch":          runtime.GOARCH,
		},
	}

	errorreport.ReportError(logger, serviceUrl, errReport)
}

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
	websocket, err := websocket.New(wsLogger, wsId, serviceUrl, params, headers, ccTargetSelectHandler, true, true, "", websocket.AgentControl)
	if err != nil {
		return nil, err
	}

	// create logger for control channel
	ccId := uuid.New().String()
	ccLogger := logger.GetControlChannelLogger(ccId)

	return controlchannel.Start(ccLogger, ccId, websocket, serviceUrl, agentType, dcTargetSelectHandler)
}

// control channel function to select correct SignalR hubs on message egress
func ccTargetSelectHandler(agentMessage am.AgentMessage) (string, error) {
	switch am.MessageType(agentMessage.MessageType) {
	case am.HealthCheck:
		return "AliveCheckAgentToBastion", nil
	default:
		return "", fmt.Errorf("unsupported message type")
	}
}

// data channel's function to select SignalR hubs base on agent message message type
func dcTargetSelectHandler(agentMessage am.AgentMessage) (string, error) {
	switch am.MessageType(agentMessage.MessageType) {
	case am.DataChannelReady:
		return "DataChannelReadyV1", nil
	case am.CloseDaemonWebsocket:
		return "CloseDaemonWebsocketV1", nil
	case am.Keysplitting:
		return "ResponseClusterToBastionV1", nil
	case am.Stream:
		return "ResponseClusterToBastionV1", nil
	case am.Error:
		return "ResponseClusterToBastionV1", nil
	default:
		return "", fmt.Errorf("unable to determine SignalR endpoint for message type: %s", agentMessage.MessageType)
	}
}

func parseFlags() error {
	// Our required registration flags
	flag.StringVar(&activationToken, "activationToken", "", "Single-use token used to register the agent")
	flag.StringVar(&apiKey, "apiKey", "", "API Key used to register the agent")

	// All optional flags
	flag.StringVar(&serviceUrl, "serviceUrl", prodServiceUrl, "Service URL to use")
	flag.StringVar(&orgId, "orgId", "", "OrgID to use")
	flag.StringVar(&targetName, "targetName", "", "Target name to use")
	flag.StringVar(&targetId, "targetId", "", "Target ID to use")
	flag.StringVar(&environmentId, "environmentId", "", "Policy environment ID to associate with agent")
	flag.StringVar(&environmentName, "environmentName", "", "Policy environment Name to associate with agent")

	// Parse any flag
	flag.Parse()

	// The environment will overwrite any flags passed
	serviceUrl = os.Getenv("SERVICE_URL")
	activationToken = os.Getenv("ACTIVATION_TOKEN")
	orgId = os.Getenv("ORG_ID")
	targetName = os.Getenv("TARGET_NAME")
	targetId = os.Getenv("TARGET_ID")
	environmentId = os.Getenv("ENVIRONMENT")
	idpProvider = os.Getenv("IDP_PROVIDER")
	idpOrgId = os.Getenv("IDP_ORG_ID")
	namespace = os.Getenv("NAMESPACE")
	apiKey = os.Getenv("API_KEY")

	// determine agent type
	if vault.InCluster() {
		agentType = Cluster
	} else {
		agentType = Bzero
	}

	// Make sure our service url is correctly formatted
	if !strings.HasPrefix(serviceUrl, "http") {
		if url, err := utils.JoinUrls("https://", serviceUrl); err != nil {
			return fmt.Errorf("error adding scheme to serviceUrl %s: %s", serviceUrl, err)
		} else {
			serviceUrl = url
		}
	}
	return nil
}

func getAgentVersion() string {
	if os.Getenv("DEV") == "true" {
		return "1.0"
	} else {
		return "$AGENT_VERSION"
	}
}

func handleRegistration(logger *logger.Logger) error {
	config, err := vault.LoadVault()
	if err != nil {
		return fmt.Errorf("could not load vault: %s", err)
	}

	// Check if there is a public key in the vault, if not then agent is not registered
	if config.Data.Namespace == "" { // PLACEHOLDER REMOVE BEFORE PUSH

		// we need either an activation token or an apikey to register the agent
		if activationToken == "" && apiKey == "" {
			return fmt.Errorf("in order to register the agent, user must provide either an activation token or api key")
		} else {
			// Save flags passed to our config
			if err := saveConfig(config); err != nil {
				return err
			}

			if vault.InCluster() {
				// Only check RBAC permissions if we are inside a cluster
				if err := rbac.CheckPermissions(logger, namespace); err != nil {
					return fmt.Errorf("error verifying agent kubernetes setup: %s", err)
				} else {
					logger.Info("Namespace and service account permissions verified.")
				}
			}

			// register the agent with bastion, if not already registered
			if err := registration.Register(logger, serviceUrl, activationToken, apiKey); err != nil {
				return err
			}
		}
	}
	return nil
}

func saveConfig(config *vault.Vault) error {
	config.Data = vault.SecretData{
		ServiceUrl:    serviceUrl,
		Namespace:     namespace,
		IdpProvider:   idpProvider,
		IdpOrgId:      idpOrgId,
		EnvironmentId: environmentId,
		AgentType:     agentType,
		TargetName:    targetName,
		Version:       getAgentVersion(),
	}

	if err := config.Save(); err != nil {
		return fmt.Errorf("error saving vault: %s", err)
	} else {
		return nil
	}
}
