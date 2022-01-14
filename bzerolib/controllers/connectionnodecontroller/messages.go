package connectionnodecontroller

// Http Requests
type CreateKubeConnectionRequest struct {
	TargetUser   string   `json:"targetUser"`
	TargetGroups []string `json:"targetGroups"`
	TargetId     string   `json:"targetId"`
}

type CreateConnectionRequest struct {
	TargetId string `json:"targetId"`
}

// Http Responses
type CreateConnectionResponse struct {
	ConnectionId string `json:"connectionId"`
}

type ConnectionAuthDetailsResponse struct {
	ConnectionNodeId     string `json:"connectionNodeId"`
	AuthToken            string `json:"authToken"`
	ConnectionServiceUrl string `json:"connectionServiceUrl"`
}

// Controller Types
type ConnectionDetailsResponse struct {
	ConnectionNodeId     string
	ConnectionServiceUrl string
	AuthToken            string
	ConnectionId         string
}
