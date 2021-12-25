package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"

	ed "crypto/ed25519"

	"github.com/google/uuid"

	"bastionzero.com/bctl/v1/bctl/agent/controlchannel"
	"bastionzero.com/bctl/v1/bctl/agent/rbac"
	"bastionzero.com/bctl/v1/bctl/agent/vault"
	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

var (
	serviceUrl, orgId, clusterName   string
	environmentId, activationToken   string
	idpProvider, namespace, idpOrgId string
	clusterId                        string
)

const (
	hubEndpoint      = "/api/v1/hub/kube-server"
	registerEndpoint = "/api/v1/kube/register-agent"
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

	if err := parseFlags(); err != nil {
		logger.Error(err)
		os.Exit(1)
	}
	logger.Infof("Information parsed for %s", clusterName)

	// Check if the agent is registered or not.  If not, generate signing keys,
	// check kube permissions and setup, and register with the Bastion.
	if err := register(logger); err != nil {
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
		"public_key":    config.Data.PublicKey,
		"agent_version": agentVersion,
		"org_id":        orgId,
		"cluster_id":    clusterId,
		"cluster_name":  clusterName,
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

	return controlchannel.Start(ccLogger, ccId, websocket, serviceUrl, dcTargetSelectHandler)
}

// control channel function to select correct SignalR hubs on message egress
func ccTargetSelectHandler(agentMessage am.AgentMessage) (string, error) {
	switch am.MessageType(agentMessage.MessageType) {
	case am.HealthCheck:
		return "AliveCheckClusterToBastion", nil
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
	}

	return "", fmt.Errorf("unable to determine SignalR endpoint for message type: %s", agentMessage.MessageType)
}

func parseFlags() error {
	// Our expected flags we need to start
	flag.StringVar(&serviceUrl, "serviceUrl", "", "Service URL to use")
	flag.StringVar(&orgId, "orgId", "", "OrgId to use")
	flag.StringVar(&clusterName, "clusterName", "", "Cluster name to use")
	flag.StringVar(&clusterId, "clusterId", "", "Cluster Id to use")
	flag.StringVar(&environmentId, "environmentId", "", "Optional environmentId to specify")
	flag.StringVar(&activationToken, "activationToken", "", "Activation Token to use to register the cluster")

	// Parse any flag
	flag.Parse()

	// The environment will overwrite any flags passed
	serviceUrl = os.Getenv("SERVICE_URL")
	activationToken = os.Getenv("ACTIVATION_TOKEN")
	orgId = os.Getenv("ORG_ID")
	clusterName = os.Getenv("CLUSTER_NAME")
	clusterId = os.Getenv("CLUSTER_ID")
	environmentId = os.Getenv("ENVIRONMENT")
	idpProvider = os.Getenv("IDP_PROVIDER")
	idpOrgId = os.Getenv("IDP_ORG_ID")
	namespace = os.Getenv("NAMESPACE")

	// Ensure we have all needed vars
	missing := []string{}
	switch {
	case serviceUrl == "":
		missing = append(missing, "serviceUrl")
		fallthrough
	case orgId == "":
		missing = append(missing, "orgId")
		fallthrough
	case clusterName == "":
		missing = append(missing, "clusterName")
		fallthrough
	case activationToken == "":
		missing = append(missing, "activationToken")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing flags: %v", missing)
	} else {
		return nil
	}
}

func getAgentVersion() string {
	if os.Getenv("DEV") == "true" {
		return "1.0"
	} else {
		return "$AGENT_VERSION"
	}
}

func register(logger *logger.Logger) error {
	logger.Info("Checking if Agent is already registered...")

	config, err := vault.LoadVault()
	if err != nil {
		return fmt.Errorf("could not load vault: %s", err)
	}

	// Check if vault is empty, if so generate a private, public key pair
	if config.IsEmpty() {
		logger.Info("This is a new agent")

		if err := rbac.CheckPermissions(logger, namespace); err != nil {
			return fmt.Errorf("error verifying agent kubernetes setup: %s", err)
		} else {
			logger.Info("Namespace and service account permissions verified.")
		}

		logger.Info("Creating cryptographic identity...")

		if publicKey, privateKey, err := ed.GenerateKey(nil); err != nil {
			return fmt.Errorf("error generating key pair: %v", err.Error())
		} else {
			pubkeyString := base64.StdEncoding.EncodeToString([]byte(publicKey))
			privkeyString := base64.StdEncoding.EncodeToString([]byte(privateKey))
			config.Data = vault.SecretData{
				PublicKey:   pubkeyString,
				PrivateKey:  privkeyString,
				OrgId:       orgId,
				ServiceUrl:  serviceUrl,
				ClusterName: clusterName,
				Namespace:   namespace,
				IdpProvider: idpProvider,
				IdpOrgId:    idpOrgId,
			}

			// Register with Bastion
			logger.Info("Cryptographic identity created.  Reaching out to BastionZero...")
			register := controlchannel.RegisterAgentMessage{
				PublicKey:      pubkeyString,
				ActivationCode: activationToken,
				AgentVersion:   getAgentVersion(),
				OrgId:          orgId,
				EnvironmentId:  environmentId,
				ClusterName:    clusterName,
				ClusterId:      clusterId,
			}

			registerJson, err := json.Marshal(register)
			if err != nil {
				msg := fmt.Errorf("error marshalling registration data: %s", err)
				return msg
			}

			// Make our POST request
			response, err := bzhttp.PostRegister(logger, "https://"+serviceUrl+registerEndpoint, "application/json", registerJson)
			if err != nil || response.StatusCode != http.StatusOK {
				rerr := fmt.Errorf("error making post request to register agent. Error: %s. Response: %+v", err, response)
				return rerr
			}

			logger.Info("Agent sucessfully Registered.  BastionZero says hi.")

			// If the registration went ok, save the config
			if err := config.Save(); err != nil {
				return fmt.Errorf("error saving vault: %s", err)
			}

			logger.Info("Agent identity saved.")

			logger.Info("Successfully completed registration.  Starting Agent normally...")
		}
	} else {
		// If the vault isn't empty, don't do anything
		logger.Info("This Agent is already registered.  Starting Agent normally...")
	}
	return nil
}
