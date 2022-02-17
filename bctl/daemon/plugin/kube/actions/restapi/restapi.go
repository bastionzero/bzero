package restapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"gopkg.in/tomb.v2"

	kuberest "bastionzero.com/bctl/v1/bctl/agent/plugin/kube/actions/restapi"
	"bastionzero.com/bctl/v1/bzerolib/bzhttp"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type RestApiAction struct {
	logger *logger.Logger

	requestId       string
	logId           string
	commandBeingRun string

	// channels for sending and recieving messages
	outputChan chan plugin.ActionWrapper
	inputChan  chan plugin.ActionWrapper
}

func New(logger *logger.Logger,
	requestId string,
	logId string,
	commandBeingRun string) (*RestApiAction, chan plugin.ActionWrapper) {

	restapi := &RestApiAction{
		logger:          logger,
		requestId:       requestId,
		logId:           logId,
		commandBeingRun: commandBeingRun,
		outputChan:      make(chan plugin.ActionWrapper, 10),
		inputChan:       make(chan plugin.ActionWrapper),
	}

	return restapi, restapi.outputChan
}

func (r *RestApiAction) ReceiveKeysplitting(wrappedAction plugin.ActionWrapper) {
	r.inputChan <- wrappedAction
}

func (r *RestApiAction) ReceiveStream(stream smsg.StreamMessage) {}

func (r *RestApiAction) Start(tmb *tomb.Tomb, writer http.ResponseWriter, request *http.Request) error {
	// this action ends at the end of this function, in order to signal that to the parent plugin,
	// we close the output channel which will close the go routine listening on it
	defer close(r.outputChan)

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
	payloadBytes, _ := json.Marshal(payload)
	r.outputChan <- plugin.ActionWrapper{
		Action:        kuberest.RestRequest,
		ActionPayload: payloadBytes,
	}

	// wait for response to our sent message
	select {
	case <-tmb.Dying():
		return nil
	case rsp := <-r.inputChan:
		// unmarshall response in rest api payload object
		var apiResponse kuberest.KubeRestApiActionResponsePayload
		if err := json.Unmarshal(rsp.ActionPayload, &apiResponse); err != nil {
			rerr := fmt.Errorf("could not unmarshal Action Response Payload: %s", err)
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

			rerr := fmt.Errorf("request failed with status code %v: %v", apiResponse.StatusCode, string(apiResponse.Content))
			r.logger.Error(rerr)
			return rerr
		}
	}

	return nil
}
