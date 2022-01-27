package registration

import (
	ed "crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"

	"bastionzero.com/bctl/v1/bctl/agent/vault"
	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

const (
	whereEndpoint = "status/where"

	// Register info
	activationTokenEndpoint      = "/api/v2/agent/token"
	registerEndpoint             = "/api/v2/agent/register"
	getConnectionServiceEndpoint = "/api/v2/connection-service/url"
)

type Registration struct {
	Logger     *logger.Logger
	Config     *vault.Vault
	ServiceUrl string
}

func Register(logger *logger.Logger, serviceUrl string, activationToken string, apiKey string) error {
	if config, err := vault.LoadVault(); err != nil {
		return fmt.Errorf("could not load vault: %s", err)

	} else if config.Data.PublicKey != "" {
		// If there's already a public key agent is already registered so don't do anything
		logger.Infof("This Agent is already registered with public key: %s", config.Data.PublicKey)
		return nil

	} else {
		// Check we have all our requried args
		if activationToken == "" && apiKey == "" {
			return fmt.Errorf("in order to register, we need either an api or activation token")
		}
		if serviceUrl == "" {
			return fmt.Errorf("serviceUrl can't be empty")
		}

		logger.Infof("Agent is not yet registered, starting registration process!")

		// Create our registration struct and actually register
		reg := Registration{
			Logger:     logger,
			Config:     config,
			ServiceUrl: serviceUrl,
		}

		// Generate and store our public, private key pair and add to config
		if err := reg.generateKeys(); err != nil {
			return err
		}

		// Complete registration with the Bastion
		if err := reg.phoneHome(activationToken, apiKey); err != nil {
			return err
		}

		// If the registration went ok, save the config
		if err := reg.Config.Save(); err != nil {
			return fmt.Errorf("error saving vault: %s", err)
		}

		logger.Info("Registration complete!  Starting Agent normally...")
		return nil
	}
}

func (r *Registration) generateKeys() error {
	// Generate public, secret key pair and convert to strings
	publicKey, privateKey, err := ed.GenerateKey(nil)
	if err != nil {
		return fmt.Errorf("error generating key pair: %v", err.Error())
	}
	r.Config.Data.PublicKey = base64.StdEncoding.EncodeToString([]byte(publicKey))
	r.Config.Data.PrivateKey = base64.StdEncoding.EncodeToString([]byte(privateKey))

	r.Logger.Info("Generated cryptographic identity")
	return nil
}

func (r *Registration) phoneHome(activationToken string, apiKey string) error {
	// If we don't have an activation token, use api key to get one
	if activationToken == "" {
		if token, err := r.getActivationToken(apiKey); err != nil {
			return err
		} else {
			activationToken = token
		}
	}

	// Register with Bastion
	r.Logger.Info("Phoning home to BastionZero...")

	if resp, err := r.getRegistrationResponse(activationToken); err != nil {
		return fmt.Errorf("error registering agent: %s", err)
	} else {
		// only replace, if values were undefined by user
		if r.Config.Data.IdpProvider == "" {
			r.Config.Data.IdpProvider = resp.OrgProvider
		}
		if r.Config.Data.IdpOrgId == "" {
			r.Config.Data.IdpOrgId = resp.OrgID
		}

		// set our remaining values
		r.Config.Data.TargetName = resp.TargetName
		r.Config.Data.TargetId = activationToken // We can save this now because it's already been used to activate

		r.Logger.Info("Agent successfully Registered.  BastionZero says hi.")
		return nil
	}
}

func (r *Registration) getActivationToken(apiKey string) (string, error) {
	r.Logger.Infof("Requesting activation token from Bastion")
	tokenEndpoint, err := bzhttp.BuildEndpoint(r.ServiceUrl, activationTokenEndpoint)
	if err != nil {
		return "", err
	}

	req := ActivationTokenRequest{
		TargetName: r.Config.Data.TargetName,
	}

	// Marshall the request
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("error marshalling activation token request: %+v", req)
	}

	headers := map[string]string{
		"X-API-KEY": apiKey,
	}
	params := map[string]string{} // no params

	resp, err := bzhttp.Post(r.Logger, tokenEndpoint, "application/json", reqBytes, headers, params)
	if err != nil {
		return "", fmt.Errorf("failed to get activation token: %s, Endpoint: %s, Response: %+v", err, tokenEndpoint, resp)
	}

	// read our activation token request body
	respBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

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

func (r *Registration) getRegistrationResponse(activationToken string) (RegistrationResponse, error) {
	var regResponse RegistrationResponse

	// if the target name was never previously set, then we default to hostname, but only Bastion knows
	// if the target name was previously set, so we send it as an additional value
	hostname, err := os.Hostname()
	if err != nil {
		return regResponse, fmt.Errorf("could not resolve hostname: %s", err)
	}

	// determine agent location
	region, err := r.whereAmI()
	if err != nil {
		return regResponse, fmt.Errorf("failed to get agent region: %s", err)
	}

	// Create our request
	req := RegistrationRequest{
		PublicKey:      r.Config.Data.PublicKey,
		ActivationCode: activationToken,
		Version:        r.Config.Data.Version,
		EnvironmentId:  r.Config.Data.EnvironmentId,
		TargetName:     r.Config.Data.TargetName,
		TargetHostName: hostname,
		AwsRegion:      region, // TODO: Should this be just Region for forward safety reasons?
	}

	// Build the endpoint we want to hit
	registrationEndpoint, err := bzhttp.BuildEndpoint(r.ServiceUrl, registerEndpoint)
	if err != nil {
		return regResponse, fmt.Errorf("error building registration url: {serviceUrl: %s, registerEndpoint: %s", r.ServiceUrl, registerEndpoint)
	}

	// Marshal the request
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return regResponse, fmt.Errorf("error marshalling register agent message for agent: %+v", req)
	}

	// Perform the request
	resp, err := bzhttp.Post(r.Logger, registrationEndpoint, "application/json", reqBytes, map[string]string{}, map[string]string{})
	if err != nil {
		return regResponse, fmt.Errorf("error on register agent: %s. Response: %+v", err, resp)
	}

	if response, err := bzhttp.Convert(resp, regResponse); err != nil {
		return regResponse, err
	} else {
		return response.(RegistrationResponse), nil
	}
}

func (r *Registration) whereAmI() (string, error) {
	// Get our region by pinging out connection-service
	connectionServiceUrl, err := r.getConnectionServiceUrlFromServiceUrl() //TODO: Question: This seems like a lot
	if err != nil {
		return "", err
	}

	whereEndpoint, err := bzhttp.BuildEndpoint(connectionServiceUrl, whereEndpoint)
	if err != nil {
		return "", err
	}

	regionResponse, err := bzhttp.Get(r.Logger, whereEndpoint, map[string]string{}, map[string]string{})
	if err != nil {
		return "", err
	}

	if regionBodyBytes, err := io.ReadAll(regionResponse.Body); err != nil {
		return "", err
	} else {
		return string(regionBodyBytes), nil
	}
}

func (r *Registration) getConnectionServiceUrlFromServiceUrl() (string, error) {
	// build our endpoint
	endpointToHit, err := bzhttp.BuildEndpoint(r.ServiceUrl, getConnectionServiceEndpoint)
	if err != nil {
		return "", fmt.Errorf("error building endpoint for get connection service request")
	}

	// make our request
	resp, err := bzhttp.Get(r.Logger, endpointToHit, map[string]string{}, map[string]string{})
	if err != nil {
		return "", fmt.Errorf("error making get request to get connection service url")
	}

	// read the response
	respBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading body on get connection service url requets")
	}

	// unmarshal the response into struct
	var getConnectionServiceResponse GetConnectionServiceResponse
	if err := json.Unmarshal(respBytes, &getConnectionServiceResponse); err != nil {
		return "", fmt.Errorf("malformed getConnectionService response: %s", err)
	}

	return getConnectionServiceResponse.ConnectionServiceUrl, nil
}
