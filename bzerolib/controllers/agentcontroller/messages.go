package agentcontroller

type RegisterAgentMessage struct {
	PublicKey       string `json:"publicKey"`
	ActivationCode  string `json:"activationCode"`
	Version         string `json:"version"`
	OrgId           string `json:"orgId"`
	EnvironmentId   string `json:"environmentId"`
	EnvironmentName string `json:"environmentName"`
	TargetName      string `json:"targetName"`
	TargetId        string `json:"targetId"`
}

// Message definitions for challenge request/response
type GetChallengeMessage struct {
	TargetId  string `json:"targetId"`
	AgentType string `json:"agentType"`
	Version   string `json:"version"`
}

type GetChallengeResponse struct {
	Challenge string `json:"challenge"`
}
