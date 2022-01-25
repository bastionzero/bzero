package connectionnodecontroller

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/utils"
)

type ConnectionNodeController struct {
	logger                *logger.Logger
	bastionUrl            string
	connectionNodeBaseUrl string
	headers               map[string]string
	params                map[string]string
}

const (
	// Kube related
	createKubeConnectionEndpoint = "/api/v2/connections/kube"

	// Db related
	createDbConnectionEndpoint = "/api/v2/connections/db"

	// Web related
	createWebConnectionEndpoint = "/api/v2/connections/web"

	// General endpoints
	getAuthDetailsEndpoint = "/api/v2/connections/$TYPE/$ID/$VERSION/connection-auth-details"
)

func New(logger *logger.Logger,
	bastionUrl string,
	connectionNodeBaseUrl string,
	headers map[string]string,
	params map[string]string) (*ConnectionNodeController, error) {

	return &ConnectionNodeController{
		logger:                logger,
		bastionUrl:            bastionUrl,
		connectionNodeBaseUrl: connectionNodeBaseUrl,
		headers:               headers,
		params:                params,
	}, nil
}

func (c *ConnectionNodeController) CreateKubeConnection(targetUser string, targetGroups []string, targetId string) (ConnectionDetailsResponse, error) {
	// Create our request
	createKubeConnectionRequest := CreateKubeConnectionRequest{
		TargetUser:   targetUser,
		TargetGroups: targetGroups,
		TargetId:     targetId,
	}

	return c.createConnection(createKubeConnectionRequest, "kube")
}

func (c *ConnectionNodeController) CreateDbConnection(targetId string) (ConnectionDetailsResponse, error) {
	// Create our request
	createDbConnectionRequest := CreateConnectionRequest{
		TargetId: targetId,
	}

	return c.createConnection(createDbConnectionRequest, "db")
}

func (c *ConnectionNodeController) CreateWebConnection(targetId string) (ConnectionDetailsResponse, error) {
	// Create our request
	createWebConnectionRequest := CreateConnectionRequest{
		TargetId: targetId,
	}

	return c.createConnection(createWebConnectionRequest, "web")
}

func (c *ConnectionNodeController) createConnection(request interface{}, connectionType string) (ConnectionDetailsResponse, error) {
	// Build the endpoint we want to hit
	endpoint := ""
	switch connectionType {
	case "kube":
		endpoint = createKubeConnectionEndpoint
	case "web":
		endpoint = createWebConnectionEndpoint
	case "db":
		endpoint = createDbConnectionEndpoint
	}

	createConnectionEndpoint, err := utils.JoinUrls(c.bastionUrl, endpoint)
	if err != nil {
		return ConnectionDetailsResponse{}, fmt.Errorf("error building url")
	}

	// Marshall the request
	msgBytes, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		return ConnectionDetailsResponse{}, fmt.Errorf("error marshalling create kube connection request for connection node: %+v", request)
	}

	// Perform the request
	httpCreateConnectionResponse, errPost := bzhttp.Post(c.logger, createConnectionEndpoint, "application/json", msgBytes, c.headers, c.params)
	if errPost != nil {
		return ConnectionDetailsResponse{}, fmt.Errorf("error on create kube connection for connection node: %s. Response: %+v", errPost, httpCreateConnectionResponse)
	}

	// Read all the bytes from the response
	createConnectionResponseBytes, readAllErr := ioutil.ReadAll(httpCreateConnectionResponse.Body)
	if readAllErr != nil {
		return ConnectionDetailsResponse{}, fmt.Errorf("error reading bytes from create connection response")
	}

	// Unmarshal the bytes
	createConnectionResponse := &CreateConnectionResponse{}
	if err := json.Unmarshal(createConnectionResponseBytes, &createConnectionResponse); err != nil {
		// TODO: Add error handling around this, we should at least retry and then bubble up the error to the user
		return ConnectionDetailsResponse{}, fmt.Errorf("error un-marshalling create connection response")
	}

	return c.createCnConnection(createConnectionResponse.ConnectionId, connectionType)
}

func (c *ConnectionNodeController) createCnConnection(connectionId string, typeOfConnection string) (ConnectionDetailsResponse, error) {
	// Now use the connectionId to get the connectionNodeId and AuthToken

	// Add our ID and type and version
	getAuthDetailsEndpointFormatted := strings.Replace(getAuthDetailsEndpoint, "$ID", connectionId, -1)
	getAuthDetailsEndpointFormatted = strings.Replace(getAuthDetailsEndpointFormatted, "$TYPE", typeOfConnection, -1)
	getAuthDetailsEndpointFormatted = strings.Replace(getAuthDetailsEndpointFormatted, "$VERSION", c.params["version"], -1)

	// Build our endpoint
	getAuthDetailsEndpoint, err := utils.JoinUrls(c.bastionUrl, getAuthDetailsEndpointFormatted)
	if err != nil {
		return ConnectionDetailsResponse{}, fmt.Errorf("error building url")
	}

	httpGetAuthDetailsResponse, errPost := bzhttp.Get(c.logger, getAuthDetailsEndpoint, c.headers, c.params)
	if errPost != nil {
		return ConnectionDetailsResponse{}, fmt.Errorf("error on getting auth details for connection node: %s. Response: %+v", errPost, httpGetAuthDetailsResponse)
	}

	// Unmarshal the bytes
	getAuthDetailsResponseBytes, readAllErr := ioutil.ReadAll(httpGetAuthDetailsResponse.Body)
	if readAllErr != nil {
		return ConnectionDetailsResponse{}, fmt.Errorf("error reading bytes from get auth details response")
	}

	// Unmarshal the bytes
	getAuthDetailsResponse := &ConnectionAuthDetailsResponse{}
	if err := json.Unmarshal(getAuthDetailsResponseBytes, &getAuthDetailsResponse); err != nil {
		// TODO: Add error handling around this, we should at least retry and then bubble up the error to the user
		return ConnectionDetailsResponse{}, fmt.Errorf("error un-marshalling create connection response")
	}

	// Return the auth details response
	return ConnectionDetailsResponse{
		ConnectionNodeId:     getAuthDetailsResponse.ConnectionNodeId,
		AuthToken:            getAuthDetailsResponse.AuthToken,
		ConnectionServiceUrl: getAuthDetailsResponse.ConnectionServiceUrl,
		ConnectionId:         connectionId,
	}, nil
}
