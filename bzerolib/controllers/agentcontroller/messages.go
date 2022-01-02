package agentcontroller

type RegisterAgentMessage struct {
	PublicKey       string `json:"publicKey"`
	ActivationCode  string `json:"activationCode"`
	AgentVersion    string `json:"agentVersion"`
	OrgId           string `json:"orgId"`
	EnvironmentId   string `json:"environmentId"`
	EnvironmentName string `json:"environmentName"`
	TargetName      string `json:"targetName"`
	TargetId        string `json:"targetId"`
	TargetType      string `json:"targetType"`
}

// Message definitions for challenge request/response
type GetChallengeMessage struct {
	OrgId      string `json:"orgId"`
	TargetId   string `json:"targetId"`
	TargetName string `json:"targetName"`
	TargetType string `json:"targetType"`
}

type GetChallengeResponse struct {
	Challenge string `json:"challenge"`
}