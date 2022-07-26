package challenge

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"bastionzero.com/bctl/v1/bzerolib/connection/httpclient"
	"golang.org/x/crypto/sha3"
)

const (
	challengeEndpoint = "/api/v2/agent/challenge"
)

type ChallengeRequest struct {
	TargetId string `json:"targetId"`
	Version  string `json:"version"`
}

type ChallengeResponse struct {
	Challenge string `json:"challenge"`
}

func Get(serviceUrl string, targetId string, version string, signingKey string) (string, error) {
	// Build our request body
	request := ChallengeRequest{
		TargetId: targetId,
		Version:  version,
	}

	requestBytes, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("error marshalling register data: %w", err)
	}

	// Initialize our http client
	options := httpclient.HTTPOptions{
		Endpoint: challengeEndpoint,
		Body:     requestBytes,
	}
	client, err := httpclient.New(nil, serviceUrl, options)
	if err != nil {
		return "", err
	}

	// Make our request
	response, err := client.Post(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to get challenge from BastionZero: %w", err)
	}

	defer response.Body.Close()
	var challengeResponse ChallengeResponse
	json.NewDecoder(response.Body).Decode(&challengeResponse)

	return Solve(challengeResponse.Challenge, signingKey)
}

// TODO: we shouldn't have multiple function implementing signing. recipe for disaster.
// but it works and so I'm leaving it for now
func Solve(content string, signingKey string) (string, error) {
	keyBytes, _ := base64.StdEncoding.DecodeString(signingKey)
	if len(keyBytes) != 64 {
		return "", fmt.Errorf("invalid private key length: %d", len(keyBytes))
	}
	privkey := ed25519.PrivateKey(keyBytes)

	hashBits := sha3.Sum256([]byte(content))

	sig := ed25519.Sign(privkey, hashBits[:])

	// Convert the signature to base64 string
	sigBase64 := base64.StdEncoding.EncodeToString(sig)

	return sigBase64, nil
}
