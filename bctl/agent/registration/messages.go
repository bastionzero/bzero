package registration

// Register logic
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
	TargetType      string `json:"agentType"`
	TargetId        string `json:"targetId"`
	Region          string `json:"region"`
}

type RegistrationResponse struct {
	TargetName  string `json:"targetName"`
	OrgID       string `json:"externalOrganizationId"`
	OrgProvider string `json:"externalOrganizationProvider"`
}

type GetConnectionServiceResponse struct {
	ConnectionServiceUrl string `json:"connectionServiceUrl"`
}
