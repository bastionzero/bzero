package restapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	kuberest "bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/restapi"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
)

// wrap the client-creation code so that during testing we can inject a mock client
var makeRequest = func(req *http.Request) (*http.Response, error) {
	client := http.Client{}
	return client.Do(req)
}

type RestApiAction struct {
	logger   *logger.Logger
	doneChan chan struct{}

	serviceAccountToken string
	kubeHost            string
	targetGroups        []string
	targetUser          string
}

func New(
	logger *logger.Logger,
	doneChan chan struct{},
	serviceAccountToken string,
	kubeHost string,
	targetGroups []string,
	targetUser string) *RestApiAction {
	return &RestApiAction{
		logger:              logger,
		doneChan:            doneChan,
		serviceAccountToken: serviceAccountToken,
		kubeHost:            kubeHost,
		targetGroups:        targetGroups,
		targetUser:          targetUser,
	}
}

func (r *RestApiAction) Kill() {}

func (r *RestApiAction) Receive(action string, actionPayload []byte) ([]byte, error) {
	defer close(r.doneChan)

	var apiRequest kuberest.KubeRestApiActionPayload
	if err := json.Unmarshal(actionPayload, &apiRequest); err != nil {
		rerr := fmt.Errorf("malformed Keysplitting Action payload %v", actionPayload)
		r.logger.Error(rerr)
		return []byte{}, rerr
	}

	// Build the request
	r.logger.Infof("Making request for %s", apiRequest.Endpoint)
	req, err := r.buildHttpRequest(apiRequest.Endpoint, apiRequest.Body, apiRequest.Method, apiRequest.Headers)
	if err != nil {
		return []byte{}, err
	}

	res, err := makeRequest(req)
	if err != nil {
		rerr := fmt.Errorf("bad response to API request: %s", err)
		r.logger.Error(rerr)
		return []byte{}, rerr
	}
	defer res.Body.Close()

	// Build the header response
	header := make(map[string][]string)
	for key, value := range res.Header {
		header[key] = value
	}

	// Parse out the body
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return []byte{}, err
	}

	// Now we need to send that data back to the client
	responsePayload := kuberest.KubeRestApiActionResponsePayload{
		StatusCode: res.StatusCode,
		RequestId:  apiRequest.RequestId,
		Headers:    header,
		Content:    bodyBytes,
	}
	responsePayloadBytes, _ := json.Marshal(responsePayload)

	return responsePayloadBytes, nil
}

func (r *RestApiAction) buildHttpRequest(endpoint, body, method string, headers map[string][]string) (*http.Request, error) {
	if toReturn, err := kubeutils.BuildHttpRequest(r.kubeHost, endpoint, body, method, headers, r.serviceAccountToken, r.targetUser, r.targetGroups); err != nil {
		r.logger.Error(err)
		return nil, err
	} else {
		return toReturn, nil
	}
}
