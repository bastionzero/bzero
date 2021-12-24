// Note: This is currently unused
package spacescontroller

import (
	"encoding/json"
	"fmt"
	"io/ioutil"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

type SpacesController struct {
	logger     *logger.Logger
	bastionUrl string
	headers    map[string]string
	params     map[string]string
}

const (
	createSpaceEndpoint = "/api/v2/spaces"
)

func New(logger *logger.Logger,
	bastionUrl string,
	headers map[string]string,
	params map[string]string) (*SpacesController, error) {

	return &SpacesController{
		logger:     logger,
		bastionUrl: bastionUrl,
		headers:    headers,
		params:     params,
	}, nil
}

func (s *SpacesController) CreateNewSpace() *CreateSpaceResponse {
	// Function to create new session
	// Create our request
	createSpaceRequest := CreateSpaceRequest{
		DisplayName:       "zli",
		ConnectionsToOpen: []string{},
	}

	// Build the endpoint we want to hit
	createSpaceEndpoint := s.bastionUrl + createSpaceEndpoint

	// Marshall the request
	msgBytes, errMarshal := json.Marshal(createSpaceRequest)
	if errMarshal != nil {
		s.logger.Error(fmt.Errorf("error marshalling create kube connection request for connection node: %v", createSpaceRequest))
		panic(errMarshal)
	}

	// Perform the request
	httpCreateSpaceResponse, errPost := bzhttp.Post(s.logger, createSpaceEndpoint, "application/json", msgBytes, s.headers, s.params)
	if errPost != nil {
		s.logger.Error(fmt.Errorf("error on create space request: %s. Response: %+v", errPost, httpCreateSpaceResponse))
		panic(errPost)
	}

	// Read all the bytes from the response
	createSpaceResponseBytes, readAllErr := ioutil.ReadAll(httpCreateSpaceResponse.Body)
	if readAllErr != nil {
		s.logger.Error(fmt.Errorf("error reading bytes from create connection response"))
		panic(readAllErr)
	}

	// Unmarshal the bytes
	createSpaceResponse := &CreateSpaceResponse{}
	if err := json.Unmarshal(createSpaceResponseBytes, &createSpaceResponse); err != nil {
		// TODO: Add error handling around this, we should at least retry and then bubble up the error to the user
		s.logger.Error(fmt.Errorf("error un-marshalling create connection response"))
		panic(err)
	}

	return createSpaceResponse
}
