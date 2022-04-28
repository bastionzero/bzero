package restapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	kuberest "bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/restapi"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type RestApiAction struct {
	logger *logger.Logger

	requestId       string
	logId           string
	commandBeingRun string
	doneChan        chan struct{}

	// channels for sending and recieving messages
	outputChan chan plugin.ActionWrapper
	inputChan  chan []byte
}

func New(logger *logger.Logger,
	outputChan chan plugin.ActionWrapper,
	doneChan chan struct{},
	requestId string,
	logId string,
	commandBeingRun string) *RestApiAction {

	return &RestApiAction{
		logger:          logger,
		requestId:       requestId,
		logId:           logId,
		commandBeingRun: commandBeingRun,
		doneChan:        doneChan,
		outputChan:      outputChan,
		inputChan:       make(chan []byte),
	}
}

func (r *RestApiAction) Kill() {
	close(r.doneChan)
}

func (r *RestApiAction) ReceiveKeysplitting(actionPayload []byte) {
	r.inputChan <- actionPayload
}

func (r *RestApiAction) ReceiveStream(stream smsg.StreamMessage) {}

func (r *RestApiAction) Start(writer http.ResponseWriter, request *http.Request) error {
	// First extract the headers out of the request
	headers := bzhttp.GetHeaders(request.Header)

	// Now extract the body
	bodyInBytes, err := bzhttp.GetBodyBytes(request.Body)
	if err != nil {
		r.logger.Error(err)
		return err
	}

	// Build the action payload
	payload := kuberest.KubeRestApiActionPayload{
		Endpoint:        request.URL.String(),
		Headers:         headers,
		Method:          request.Method,
		Body:            string(bodyInBytes), // fix this
		RequestId:       r.requestId,
		LogId:           r.logId,
		CommandBeingRun: r.commandBeingRun,
	}
	// send action payload to plugin to be sent to agent
	r.outputChan <- plugin.ActionWrapper{
		Action:        kuberest.RestRequest,
		ActionPayload: payload,
	}

	// wait for response to our sent message
	select {
	case <-r.doneChan:
		return nil
	case <-request.Context().Done():
		close(r.doneChan)
		return fmt.Errorf("request context cancelled")
	case rspBytes := <-r.inputChan:
		defer close(r.doneChan)

		var apiResponse kuberest.KubeRestApiActionResponsePayload
		if err := json.Unmarshal(rspBytes, &apiResponse); err != nil {
			rerr := fmt.Errorf("could not unmarshal Action Response Payload: %s", string(rspBytes))
			r.logger.Error(rerr)
			return rerr
		}

		// extract and build our writer headers
		for name, values := range apiResponse.Headers {
			for _, value := range values {
				if name != "Content-Length" {
					writer.Header().Set(name, value)
				}
			}
		}

		// write response to user
		writer.Write(apiResponse.Content)

		// if there was an error in the response
		if apiResponse.StatusCode != http.StatusOK {
			writer.WriteHeader(http.StatusInternalServerError)

			rerr := fmt.Errorf("request failed with status code %d: %s", apiResponse.StatusCode, string(apiResponse.Content))
			r.logger.Error(rerr)
			return rerr
		}
	}

	return nil
}
