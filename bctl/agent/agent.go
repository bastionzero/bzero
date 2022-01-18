package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"

	"bastionzero.com/bctl/v1/bctl/agent/controlchannel"
	"bastionzero.com/bctl/v1/bctl/agent/rbac"
	"bastionzero.com/bctl/v1/bctl/agent/vault"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

const (
	prodServiceUrl = "https://cloud.bastionzero.com/"
)

var (
	serviceUrl, orgId                string
	environmentId, environmentName   string
	activationToken, apiKey          string
	idpProvider, namespace, idpOrgId string
	targetName, targetId, agentType  string
)

// Keep these as strings so they can be send as query params
// They are then matched to the correct enum on Bastion by parsing the int
const (
	Cluster string = "0"
	Bzero   string = "1"
)

type TargetType string

const (
	BZero TargetType = "bzero"
	Kube  TargetType = "kube"
)

func main() {
	// grab agent version
	agentVersion := getAgentVersion()

	// setup our loggers
	logger, err := logger.New(logger.Debug, "")
	if err != nil {
		return
	}
	logger.AddAgentVersion(agentVersion)
	logger.Infof("BastionZero Agent version %s starting up...", agentVersion)

	parseFlags()
	logger.Infof("Information parsed for %s", targetName)

	// Check if the agent is registered or not.  If not, generate signing keys,
	// check kube permissions and setup, and register with the Bastion.
	if err := handleRegistration(logger); err != nil {
		logger.Error(err)
		return
	}

	// Connect the control channel to BastionZero
	logger.Info("Creating connection to BastionZero...")
	startControlChannel(logger, agentVersion)

	logger.Info("Connection created successfully. Listening for incoming commands...")

	// Sleep forever because otherwise kube will endlessly try restarting
	// Ref: https://stackoverflow.com/questions/36419054/go-projects-main-goroutine-sleep-forever
	select {}
}

func startControlChannel(logger *logger.Logger, agentVersion string) error {
	// Load in our saved config
	config, err := vault.LoadVault()
	if err != nil {
		return fmt.Errorf("failed to retrieve vault: %s", err)
	}

	// Create our headers and params, headers are empty
	headers := make(map[string]string)

	// Make and add our params
	params := map[string]string{
		"public_key":  config.Data.PublicKey,
		"version":     agentVersion,
		"org_id":      orgId,
		"target_id":   targetId,
		"target_name": targetName,
		"agent_type":  agentType,
	}

	// create a websocket
	wsId := uuid.New().String()
	wsLogger := logger.GetWebsocketLogger(wsId) // TODO: replace with actual connectionId
	websocket, err := websocket.New(wsLogger, wsId, serviceUrl, params, headers, ccTargetSelectHandler, true, true, "", websocket.ClusterAgentControl)
	if err != nil {
		return err
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

func parseFlags() {
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

	// determine agent type
	if vault.InCluster() {
		agentType = Cluster
	} else {
		agentType = Bzero
	}
}

func getAgentVersion() string {
	if os.Getenv("DEV") == "true" {
		return "1.0"
	} else {
		return "$AGENT_VERSION"
	}
}

func handleRegistration(logger *logger.Logger) error {
	// if there are more than zero flags, register
	if flag.NFlag() > 0 {
		if activationToken == "" && apiKey == "" {
			return fmt.Errorf("in order to register the agent, user must provide either an activation token or api key")
		} else {
			if vault.InCluster() {
				// Only check RBAC permissions if we are inside a cluster
				if err := rbac.CheckPermissions(logger, namespace); err != nil {
					return fmt.Errorf("error verifying agent kubernetes setup: %s", err)
				} else {
					logger.Info("Namespace and service account permissions verified.")
				}
			}

			// register the agent with bastion, if not already registered
			register(logger)
		}
	}
	return nil
}
