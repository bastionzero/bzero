package agentcontroller

import (
	ed "crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/utils"
	"golang.org/x/crypto/sha3"
)

type AgentController struct {
	logger                *logger.Logger
	bastionUrl            string
	connectionNodeBaseUrl string
	headers               map[string]string
	params                map[string]string
	agentType             int
}

const (
	challengeEndpoint = "/api/v2/agent/challenge"
)

func New(logger *logger.Logger,
	bastionUrl string,
	headers map[string]string,
	params map[string]string,
	agentType string) *AgentController {

	// Build the endpoint we want to hit
	bastionUrlFormatted, err := utils.JoinUrls("https://", bastionUrl)
	if err != nil {
		logger.Error(fmt.Errorf("error building url"))
		panic(err)
	}

	agentTypeInt, errParse := strconv.Atoi(agentType)
	if errParse != nil {
		logger.Error(fmt.Errorf("error on parsing agentType to enum int: %s", err))
		panic(errParse)
	}

	return &AgentController{
		logger:     logger,
		bastionUrl: bastionUrlFormatted,
		headers:    headers,
		params:     params,
		agentType:  agentTypeInt,
	}
}
func (c *AgentController) GetChallenge(orgId string, targetId string, targetName string, privateKey string, version string) (string, error) {
	// Get challenge
	challengeRequest := GetChallengeMessage{
		OrgId:      orgId,
		TargetId:   targetId,
		TargetName: targetName,
		AgentType:  c.agentType,
		Version:    version,
	}

	challengeJson, err := json.Marshal(challengeRequest)
	if err != nil {
		return "", fmt.Errorf("error marshalling register data: %s", err)
	}

	// Build the endpoint we want to hit
	challengeEndpointFormatted, err := utils.JoinUrls(c.bastionUrl, challengeEndpoint)
	if err != nil {
		c.logger.Error(fmt.Errorf("error building url"))
		panic(err)
	}

	// Make our POST request
	response, err := bzhttp.PostContent(c.logger, challengeEndpointFormatted, "application/json", challengeJson)
	if err != nil {
		return "", fmt.Errorf("error making post request to challenge agent. Error: %s. Response: %+v", err, response)
	}
	defer response.Body.Close()

	// Extract the challenge
	responseDecoded := GetChallengeResponse{}
	json.NewDecoder(response.Body).Decode(&responseDecoded)

	// Solve Challenge
	return SignString(privateKey, responseDecoded.Challenge)
}

func SignString(privateKey string, content string) (string, error) {
	keyBytes, _ := base64.StdEncoding.DecodeString(privateKey)
	if len(keyBytes) != 64 {
		return "", fmt.Errorf("invalid private key length: %v", len(keyBytes))
	}
	privkey := ed.PrivateKey(keyBytes)

	hashBits := sha3.Sum256([]byte(content))

	sig := ed.Sign(privkey, hashBits[:])

	// Convert the signature to base64 string
	sigBase64 := base64.StdEncoding.EncodeToString(sig)

	return sigBase64, nil
}
