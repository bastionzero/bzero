package spacescontroller

// Http Requests
type CreateSpaceRequest struct {
	DisplayName       string   `json:"displayName"`
	ConnectionsToOpen []string `json:"connectionsToOpen"`
}

// Http Responses
type CreateSpaceResponse struct {
	SpaceId string `json:"SpaceId"`
}
