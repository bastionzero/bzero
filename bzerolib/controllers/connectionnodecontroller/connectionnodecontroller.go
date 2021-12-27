package connectionnodecontroller

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
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
	createDbConnectionEndpoint = "api/v2/connection/db"

	// General endpoints
	getAuthDetailsEndpoint = "/api/v2/connections/$ID/connection-auth-details"
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

func (c *ConnectionNodeController) CreateKubeConnection(targetUser string, targetGroups []string, targetId string) ConnectionDetailsResponse {
	// Create our request
	createKubeConnectionRequest := CreateKubeConnectionRequest{
		TargetUser:   targetUser,
		TargetGroups: targetGroups,
		TargetId:     targetId,
	}

	// Build the endpoint we want to hit
	createConnectionEndpoint := c.bastionUrl + createKubeConnectionEndpoint

	// Marshall the request
	msgBytes, errMarshal := json.Marshal(createKubeConnectionRequest)
	if errMarshal != nil {
		c.logger.Error(fmt.Errorf("error marshalling create kube connection request for connection node: %v", createKubeConnectionRequest))
		panic(errMarshal)
	}

	// Perform the request
	httpCreateConnectionResponse, errPost := bzhttp.Post(c.logger, createConnectionEndpoint, "application/json", msgBytes, c.headers, c.params)
	if errPost != nil {
		c.logger.Error(fmt.Errorf("error on create kube connection for connection node: %s. Response: %+v", errPost, httpCreateConnectionResponse))
		panic(errPost)
	}

	// Read all the bytes from the response
	createConnectionResponseBytes, readAllErr := ioutil.ReadAll(httpCreateConnectionResponse.Body)
	if readAllErr != nil {
		c.logger.Error(fmt.Errorf("error reading bytes from create connection response"))
		panic(readAllErr)
	}

	// Unmarshal the bytes
	createConnectionResponse := &CreateConnectionResponse{}
	if err := json.Unmarshal(createConnectionResponseBytes, &createConnectionResponse); err != nil {
		// TODO: Add error handling around this, we should at least retry and then bubble up the error to the user
		c.logger.Error(fmt.Errorf("error un-marshalling create connection response"))
		panic(err)
	}

	return c.createCnConnection(createConnectionResponse.ConnectionId)
}

func (c *ConnectionNodeController) CreateDbConnection(targetId string) ConnectionDetailsResponse {
	// Create our request
	createDbConnectionRequest := CreateKubeConnectionRequest{
		TargetId: targetId,
	}

	// Build the endpoint we want to hit
	createConnectionEndpoint := c.bastionUrl + createDbConnectionEndpoint

	// Marshall the request
	msgBytes, errMarshal := json.Marshal(createDbConnectionRequest)
	if errMarshal != nil {
		c.logger.Error(fmt.Errorf("error marshalling create db connection request for connection node: %v", createDbConnectionRequest))
		panic(errMarshal)
	}

	// Perform the request
	httpCreateConnectionResponse, errPost := bzhttp.Post(c.logger, createConnectionEndpoint, "application/json", msgBytes, c.headers, c.params)
	if errPost != nil {
		c.logger.Error(fmt.Errorf("error on create db connection for connection node: %s. Response: %+v", errPost, httpCreateConnectionResponse))
		panic(errPost)
	}

	// Read all the bytes from the response
	createConnectionResponseBytes, readAllErr := ioutil.ReadAll(httpCreateConnectionResponse.Body)
	if readAllErr != nil {
		c.logger.Error(fmt.Errorf("error reading bytes from create connection response"))
		panic(readAllErr)
	}

	// Unmarshal the bytes
	createConnectionResponse := &CreateConnectionResponse{}
	if err := json.Unmarshal(createConnectionResponseBytes, &createConnectionResponse); err != nil {
		// TODO: Add error handling around this, we should at least retry and then bubble up the error to the user
		c.logger.Error(fmt.Errorf("error un-marshalling create connection response"))
		panic(err)
	}

	return c.createCnConnection(createConnectionResponse.ConnectionId)
}

func (c *ConnectionNodeController) createCnConnection(connectionId string) ConnectionDetailsResponse {
	// Now use the connectionId to get the connectionNodeId and AuthToken
	getAuthDetailsEndpoint := c.bastionUrl + strings.Replace(getAuthDetailsEndpoint, "$ID", connectionId, -1)
	httpGetAuthDetailsResponse, errPost := bzhttp.Get(c.logger, getAuthDetailsEndpoint, c.headers, c.params)
	if errPost != nil {
		c.logger.Error(fmt.Errorf("error on getting auth details for connection node: %s. Response: %+v", errPost, httpGetAuthDetailsResponse))
		panic(errPost)
	}

	// Unmarshal the bytes
	getAuthDetailsResponseBytes, readAllErr := ioutil.ReadAll(httpGetAuthDetailsResponse.Body)
	if readAllErr != nil {
		c.logger.Error(fmt.Errorf("error reading bytes from get auth details response"))
		panic(readAllErr)
	}

	// Unmarshal the bytes
	getAuthDetailsResponse := &ConnectionAuthDetailsResponse{}
	if err := json.Unmarshal(getAuthDetailsResponseBytes, &getAuthDetailsResponse); err != nil {
		// TODO: Add error handling around this, we should at least retry and then bubble up the error to the user
		c.logger.Error(fmt.Errorf("error un-marshalling create connection response"))
		panic(err)
	}

	// Return the auth details response
	return ConnectionDetailsResponse{
		ConnectionNodeId: getAuthDetailsResponse.ConnectionNodeId,
		AuthToken:        getAuthDetailsResponse.AuthToken,
		ConnectionId:     connectionId,
	}
}
