package connectionnodecontroller

import (
	"fmt"
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
	closeConnectionEndpoint = "/api/v2/connections/$ID/close"
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

func (c *ConnectionNodeController) CloseConnection(connectionId string) error {
	endpoint := strings.Replace(closeConnectionEndpoint, "$ID", connectionId, -1)
	closeConnectionEndpointToHit, err := bzhttp.BuildEndpoint(c.bastionUrl, endpoint)
	if err != nil {
		return fmt.Errorf("error building url")
	}
	patchResponse, errPost := bzhttp.Patch(c.logger, closeConnectionEndpointToHit, c.headers, c.params)
	if errPost != nil {
		return fmt.Errorf("error closing connection: %s. Response: %+v", errPost, patchResponse)
	}
	return nil
}
