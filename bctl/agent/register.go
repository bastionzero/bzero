package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"

	ed "crypto/ed25519"

	"bastionzero.com/bctl/v1/bctl/agent/vault"
	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/utils"
)

type ActivationTokenRequest struct {
	TargetName      string `json:"targetName"`
	EnvironmentId   string `json:"environmentId"`
	EnvironmentName string `json:"environmentName"`
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
	TargetType      string `json:"agentType"`
	// OrgId           string `json:"orgId"`
	// TargetId        string `json:"targetId"`
}

type RegistrationResponse struct {
	TargetName  string `json:"targetName"`
	OrgID       string `json:"externalOrganizationId"`
	OrgProvider string `json:"externalOrganizationProvider"`
}

const (
	activationTokenEndpoint = "/api/v2/targets/bzero"
	registerEndpoint        = "/api/v2/agent/register-agent" // TODO: change this to register
)

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

	req := ActivationTokenRequest{
		TargetName:      targetName,
		EnvironmentId:   environmentId,
		EnvironmentName: environmentName,
	}

	// Marshall the request
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("error marshalling activation token request: %+v", req)
	}

	if resp, err := bzhttp.Post(logger, tokenEndpoint, "appplication/json", reqBytes, map[string]string{}, map[string]string{}); err != nil {
		return "", fmt.Errorf("failed to get activation token: %s", err)
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

	// Create our request
	req := RegistrationRequest{
		PublicKey:       publicKey,
		ActivationCode:  activationToken,
		Version:         getAgentVersion(),
		EnvironmentId:   environmentId,
		EnvironmentName: environmentName,
		TargetName:      targetName,
		TargetType:      agentType,
	}

	// Build the endpoint we want to hit
	registrationEndpoint, err := utils.JoinUrls(serviceUrl, registerEndpoint)
	if err != nil {
		return regResponse, fmt.Errorf("error building registration url: {serviceUrl: %s, registerEndpoint: %s", serviceUrl, registerEndpoint)
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
