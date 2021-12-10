package websocket

import (
	ed "crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"golang.org/x/crypto/sha3"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

const (
	challengeEndpoint = "/api/v1/kube/get-challenge"
)

func newChallenge(logger *logger.Logger, orgId string, clusterId string, clusterName string, serviceUrl string, privateKey string) (string, error) {
	// Get challenge
	challengeRequest := GetChallengeMessage{
		OrgId:       orgId,
		ClusterId:   clusterId,
		ClusterName: clusterName,
	}

	challengeJson, err := json.Marshal(challengeRequest)
	if err != nil {
		return "", fmt.Errorf("error marshalling register data: %s", err)
	}

	// Make our POST request
	response, err := bzhttp.PostContent(logger, "https://"+serviceUrl+challengeEndpoint, "application/json", challengeJson)
	if err != nil {
		return "", fmt.Errorf("error making post request to challenge agent. Error: %s. Response: %+v", err, response)
	}
	defer response.Body.Close()

	// Extract the challenge
	responseDecoded := GetChallengeResponse{}
	json.NewDecoder(response.Body).Decode(&responseDecoded)

	// Solve Challenge
	return signString(privateKey, responseDecoded.Challenge)
}

func signString(privateKey string, content string) (string, error) {
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
