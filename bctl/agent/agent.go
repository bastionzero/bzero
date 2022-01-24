package main

import (
	ed "crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"bastionzero.com/bctl/v1/bctl/agent/controlchannel"
	"bastionzero.com/bctl/v1/bctl/agent/rbac"
	"bastionzero.com/bctl/v1/bctl/agent/vault"
	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/utils"
)

const (
	prodServiceUrl = "https://cloud.bastionzero.com/"
	whereEndpoint  = "status/where"

	// Register info
	activationTokenEndpoint = "/api/v2/agent/token"
	registerEndpoint        = "/api/v2/agent/register"
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
	Cluster string = "cluster"
	Bzero   string = "bzero"
)

type ActivationTokenRequest struct {
	TargetName string `json:"targetName"`
}

type ActivationTokenResponse struct {
	ActivationToken string `json:"activationToken"`
}

type RegistrationRequest struct {
	PublicKey       string `json:"publicKey"`
	ActivationCode  string `json:"activationCode"`
	Version         string `json:"version"`
	EnvironmentId   string `json:"environmentId"`
	EnvironmentName string `json:"environmentName"`
	TargetName      string `json:"targetName"`
	TargetHostName  string `json:"targetHostName"`
	TargetId        string `json:"targetId"`
	AwsRegion       string `json:"awsRegion"`
}

type RegistrationResponse struct {
	TargetName  string `json:"targetName"`
	OrgID       string `json:"externalOrganizationId"`
	OrgProvider string `json:"externalOrganizationProvider"`
}

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
	apiKey = os.Getenv("API_KEY")

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
	// if there are more than zero flags, register, or if the api key has been passed via an env var
	if flag.NFlag() > 0 || apiKey != "" {
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
			err := register(logger)
			if err != nil {
				panic(err)
			}
		}
	}
	return nil
}

func register(logger *logger.Logger) error {
	logger.Info("Checking if Agent is already registered...")

	config, err := vault.LoadVault()
	if err != nil {
		return fmt.Errorf("could not load vault: %s", err)
	}

	// Check if vault is empty, if so generate a private, public key pair
	if config.IsEmpty() {
		logger.Info("This is a new agent, starting registration...")

		// Generate public, secret key pair and convert to strings
		publicKey, privateKey, err := ed.GenerateKey(nil)
		if err != nil {
			return fmt.Errorf("error generating key pair: %v", err.Error())
		}
		pubkeyString := base64.StdEncoding.EncodeToString([]byte(publicKey))
		seckeyString := base64.StdEncoding.EncodeToString([]byte(privateKey))
		logger.Info("Generated cryptographic identity")

		// If we don't have an activation token, use api key to get one
		if activationToken == "" {
			if token, err := getActivationToken(logger); err != nil {
				return err
			} else {
				activationToken = token
			}
		}

		// Register with Bastion
		logger.Info("Registering with BastionZero...")

		if resp, err := getRegistrationResponse(logger, pubkeyString); err != nil {
			return fmt.Errorf("error registering agent: %s", err)
		} else {

			// only replace, if values were undefined by user
			if idpProvider == "" {
				idpProvider = resp.OrgProvider
			}

			if idpOrgId == "" {
				idpOrgId = resp.OrgID
			}

			// store data in config
			config.Data = vault.SecretData{
				PublicKey:   pubkeyString,
				PrivateKey:  seckeyString,
				ServiceUrl:  serviceUrl,
				TargetName:  resp.TargetName,
				Namespace:   namespace,
				IdpProvider: idpProvider,
				IdpOrgId:    idpOrgId,
				TargetId:    activationToken,
			}
			logger.Info("Agent successfully Registered.  BastionZero says hi.")

			// If the registration went ok, save the config
			if err := config.Save(); err != nil {
				return fmt.Errorf("error saving vault: %s", err)
			}
		}
		logger.Info("Successfully completed registration.  Starting Agent normally...")
	} else {
		// If the vault isn't empty, don't do anything
		logger.Info("This Agent is already registered.  Starting Agent normally...")
	}
	return nil
}

func getActivationToken(logger *logger.Logger) (string, error) {

	tokenEndpoint, err := utils.JoinUrls(serviceUrl, activationTokenEndpoint)
	if err != nil {
		return "", err
	}

	// Always make sure we've added a scheme
	if !strings.Contains(tokenEndpoint, "https://") {
		tokenEndpoint, err = utils.JoinUrls("https://", tokenEndpoint)
		if err != nil {
			return "", fmt.Errorf("error adding scheme to url: {serviceUrl: %s, tokenEndpoint: %s", serviceUrl, tokenEndpoint)
		}
	}

	req := ActivationTokenRequest{
		TargetName: targetName,
	}

	// Marshall the request
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("error marshalling activation token request: %+v", req)
	}

	headers := map[string]string{
		"X-API-KEY": apiKey,
	}

	if resp, err := bzhttp.Post(logger, tokenEndpoint, "application/json", reqBytes, headers, map[string]string{}); err != nil {
		return "", fmt.Errorf("failed to get activation token: %s, Endpoint: %s, Response: %+v", err, tokenEndpoint, resp)
	} else {

		// read our activation token request body
		respBytes, _ := ioutil.ReadAll(resp.Body)

		var tokenResponse ActivationTokenResponse
		if err := json.Unmarshal(respBytes, &tokenResponse); err != nil {
			return "", fmt.Errorf("malformed activation token response: %s", err)
		}

		if tokenResponse.ActivationToken == "" {
			return "", fmt.Errorf("activation request returned empty response")
		} else {
			return tokenResponse.ActivationToken, nil
		}
	}
}

func getRegistrationResponse(logger *logger.Logger, publicKey string) (RegistrationResponse, error) {

	var regResponse RegistrationResponse

	hostname, err := os.Hostname()
	if err != nil {
		return regResponse, fmt.Errorf("could not resolve hostname")
	}

	// Get our region by pinging out connection-service
	connectionServiceUrl := getConnectionServiceUrlFromServiceUrl(serviceUrl)
	whereEndpoint, whereErr := utils.JoinUrls(connectionServiceUrl, whereEndpoint)
	if whereErr != nil {
		return regResponse, whereErr
	}
	whereEndpointWScheme, whereErrWScheme := utils.JoinUrls("https://", whereEndpoint)
	if whereErrWScheme != nil {
		return regResponse, whereErrWScheme
	}
	regionResponse, errRegion := bzhttp.Get(logger, whereEndpointWScheme, map[string]string{}, map[string]string{})
	if errRegion != nil {
		return regResponse, errRegion
	}

	regionBodyBytes, regionReadError := io.ReadAll(regionResponse.Body)
	if regionReadError != nil {
		log.Fatal(err)
	}
	region := string(regionBodyBytes)

	// Create our request
	req := RegistrationRequest{
		PublicKey:       publicKey,
		ActivationCode:  activationToken,
		Version:         getAgentVersion(),
		EnvironmentId:   environmentId,
		EnvironmentName: environmentName,
		TargetName:      targetName,
		TargetHostName:  hostname,
		TargetId:        activationToken, // The activation token is the new targetId
		AwsRegion:       region,
	}

	// Build the endpoint we want to hit
	registrationEndpoint, err := utils.JoinUrls(serviceUrl, registerEndpoint)
	if err != nil {
		return regResponse, fmt.Errorf("error building registration url: {serviceUrl: %s, registerEndpoint: %s", serviceUrl, registerEndpoint)
	}

	// Always make sure we've added a scheme
	if !strings.Contains(registrationEndpoint, "https://") {
		registrationEndpoint, err = utils.JoinUrls("https://", registrationEndpoint)
		if err != nil {
			return regResponse, fmt.Errorf("error adding scheme to url: {serviceUrl: %s, registerEndpoint: %s", serviceUrl, registerEndpoint)
		}
	}

	// Marshall the request
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return regResponse, fmt.Errorf("error marshalling register agent message for agent: %+v", req)
	}

	// Perform the request
	if resp, err := bzhttp.Post(logger, registrationEndpoint, "application/json", reqBytes, map[string]string{}, map[string]string{}); err != nil {
		return regResponse, fmt.Errorf("error on register agent: %s. Response: %+v", err, resp)
	} else {
		// read our activation token request body
		respBytes, _ := ioutil.ReadAll(resp.Body)

		if err := json.Unmarshal(respBytes, &regResponse); err != nil {
			return regResponse, fmt.Errorf("malformed registration response: %s", err)
		}

		return regResponse, nil
	}
}

func getConnectionServiceUrlFromServiceUrl(serviceUrl string) string {
	toReplaceRegex := regexp.MustCompile(`([a-z]*).bastionzero.com`)
	groupMaches := toReplaceRegex.FindStringSubmatch(serviceUrl)
	prefix := groupMaches[1] // Extract cloud from cloud.bastionzero.com
	connectUrl := fmt.Sprintf("%s-connection-service", prefix)
	return strings.Replace(serviceUrl, prefix, connectUrl, -1)
}
