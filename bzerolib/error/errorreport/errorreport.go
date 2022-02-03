package errorreport

import (
	"encoding/json"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

const (
	// Error endpoint
	errorEndpoint = "/api/v2/agent/error"
)

type ErrorReport struct {
	Reporter  string      `json:"reporter"`
	Timestamp string      `json:"timestamp"`
	State     interface{} `json:"state"`
	Message   string      `json:"message"`
	Logs      string      `json:"logs"`
}

func ReportError(logger *logger.Logger, serviceUrl string, errReport ErrorReport) {
	// make our state a string
	stateBytes, err := json.Marshal(errReport.State)
	if err != nil {
		logger.Errorf("error marshalling error report: %+v", errReport)
		return
	}
	errReport.State = string(stateBytes)

	endpoint, err := bzhttp.BuildEndpoint(serviceUrl, errorEndpoint)
	if err != nil {
		logger.Errorf("failed to report error: %s", errReport)
	}

	// Marshall the request
	errBytes, err := json.Marshal(errReport)
	if err != nil {
		logger.Errorf("error marshalling error report: %+v", errReport)
		return
	}

	if resp, err := bzhttp.Post(logger, endpoint, "application/json", errBytes, map[string]string{}, map[string]string{}); err != nil {
		logger.Errorf("failed to report error: %s, Endpoint: %s, Request: %+v, Response: %+v", err, endpoint, errReport, resp)
	}
}
