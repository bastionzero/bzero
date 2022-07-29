package signalr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"time"

	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/connection/broker"
	"bastionzero.com/bctl/v1/bzerolib/connection/httpclient"
	"bastionzero.com/bctl/v1/bzerolib/connection/signalr/invocation"
	"bastionzero.com/bctl/v1/bzerolib/connection/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"gopkg.in/tomb.v2"
)

const (
	negotiateEndpoint = "negotiate"
)

type SignalR struct {
	tmb      tomb.Tomb
	logger   *logger.Logger
	doneChan chan struct{}

	client   *websocket.Websocket
	inbound  chan *SignalRMessage
	outbound chan *am.AgentMessage

	// Function for choosing target method
	targetSelector func(am.AgentMessage) (string, error)

	// Used for broadcasting the same recieved agent message to any number of
	// listeners
	broadcaster *broker.Broker

	// Thread-safe implementation for tracking whether SignalR messages
	// are received/processed successfully or not
	invocator *invocation.Invocation
}

func New(
	logger *logger.Logger,
	client *websocket.Websocket,
	targetSelectHandler func(msg am.AgentMessage) (string, error),
) *SignalR {
	return &SignalR{
		logger:      logger,
		client:      client,
		doneChan:    make(chan struct{}),
		inbound:     make(chan *SignalRMessage, 200),
		outbound:    make(chan *am.AgentMessage, 200),
		broadcaster: broker.New(),
		invocator:   invocation.New(),
	}
}

func (s *SignalR) Close(reason error) {
	if s.tmb.Alive() {
		s.client.Close()

		s.tmb.Kill(reason)
		s.tmb.Wait()
	}
}

func (s *SignalR) Done() <-chan struct{} {
	return s.doneChan
}

func (s *SignalR) Inbound() <-chan *SignalRMessage {
	return s.inbound
}

func (s *SignalR) Subscribe(id string, channel broker.IChannel) {
	s.broadcaster.Subscribe(id, channel)
}

func (s *SignalR) Unsubscribe(id string) {
	s.broadcaster.Unsubscribe(id)
}

func (s *SignalR) Receive(msg am.AgentMessage) {
	s.outbound <- &msg
}

func (s *SignalR) Connect(targetUrl string, params map[string][]string) error {
	// Reset variables
	s.tmb = tomb.Tomb{}
	s.doneChan = make(chan struct{})

	// Add the client protocol for SignalR
	// LUCIE: figure out if I actually need this header; took it from bzhttp
	// params["clientProtocol"] = []string{"1.5"}

	// Make negotiation call to initiate handshake
	if err := s.negotiate(targetUrl, params); err != nil {
		return fmt.Errorf("failed to complete SignalR handshake: %w", err)
	}

	// Build our Url
	u, err := buildUrl(targetUrl, params)
	if err != nil {
		return err
	}

	s.logger.Infof("URL %+v", u)

	// Connect to our endpoint
	if err := s.client.Dial(u); err != nil {
		return fmt.Errorf("failed to connect to endpoint %s: %w", u.String(), err)
	}

	// Negotiate our SignalR version
	// Ref: https://stackoverflow.com/questions/65214787/signalr-websockets-and-go
	versionMessageBytes := append([]byte(`{"protocol": "json","version": 1}`), signalRMessageTerminatorByte)
	if err := s.client.Send(versionMessageBytes); err != nil {
		s.client.Close()
		return fmt.Errorf("failed to negotiate SignalR version: %w", err)
	}

	// If the handshake was successful, then we've made our connection and we can
	// start listening and sending on it
	s.tmb.Go(func() error {
		defer s.logger.Info("Connection is dead")
		defer close(s.doneChan)
		defer s.client.Close()

		// Wrap and send outbound messages
		s.tmb.Go(func() error {
			for {
				select {
				case <-s.tmb.Dying():
					return nil
				case msg := <-s.outbound:
					if err := s.wrap(*msg); err != nil {
						s.logger.Errorf("failed to send agent message: %s", err)
					}
				}
			}
		})

		// Unwrap and forward inbound messages
		s.logger.Info("WEVE STARTING LISTENING")
		for {
			select {
			case <-s.tmb.Dying():
				return nil
			case <-s.client.Done():
				s.logger.Infof("detected closure of underlying websocket")
				return fmt.Errorf("closed websocket")
			case rawMsg := <-s.client.Inbound():
				s.logger.Infof("TRAPPED?")
				if err := s.unwrap(*rawMsg); err != nil {
					s.logger.Errorf("error processing raw message from websocket: %s", err)
				}
				s.logger.Infof("NOPE")
			}
		}
	})

	return nil
}

func (s *SignalR) negotiate(connectionUrl string, params map[string][]string) error {
	// LUCIE: what do we need params to be here? DOES NEGOTIATE USE THE PARAMS?!
	options := httpclient.HTTPOptions{
		Endpoint: negotiateEndpoint,
		Params:   params,
	}

	combo, _ := url.Parse(connectionUrl)
	combo.Path = path.Join(combo.Path, options.Endpoint)

	s.logger.Infof("PARAMS: %+v, ENDPOINT: %s", params, combo.String())
	client, err := httpclient.New(s.logger, connectionUrl, options)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context if we're dying but don't keep this go routine around forever
	go func() {
		select {
		case <-s.tmb.Dying():
			cancel()
		case <-time.After(httpclient.HTTPTimeout):
			return
		}
	}()

	// Make negotiate call
	_, err = client.Post(ctx)
	if err != nil {
		return fmt.Errorf("failed on call to negotiate: %s", err)
	}

	// var negotiateResponse struct {
	// 	ConnectionId string `json:"connectionId"`
	// }
	// body, err := ioutil.ReadAll(response.Body)
	// if err != nil {
	// 	return fmt.Errorf("failed to read negotiate response")
	// }
	// if err := json.Unmarshal()

	// // LUCIE: might need this here
	// w.params["id"] = m["connectionId"].(string)
	// w.params["clientProtocol"] = "1.5"
	// w.params["transport"] = "WebSockets"

	// In which case we might need this too:
	// type AgentConnectedMessage struct {
	// 	ConnectionId string `json:"connectionId"`
	// }

	return nil
}

func (s *SignalR) unwrap(raw []byte) error {
	// We may have received multiple messages in one
	splitMessages := bytes.Split(raw, []byte{signalRMessageTerminatorByte})

	for _, rawMessage := range splitMessages {
		// Ignore empty slices AND empty json "{}"
		if len(rawMessage) <= 2 {
			continue
		}

		// Only grab the message type so we can switch on it
		var signalRMessageType MessageTypeOnly
		if err := json.Unmarshal(rawMessage, &signalRMessageType); err != nil {
			return fmt.Errorf("error unmarshalling SignalR message: %s", string(rawMessage))
		}

		switch SignalRMessageType(signalRMessageType.Type) {
		// This SignalR close message can be thrown on `OnConnectedAsync` as a result of
		//  of failed param validation
		case Close:
			s.logger.Infof("received SignalR message to close the connection")
			s.Close(fmt.Errorf("received SignalR message to close the connection"))

		// These messages let us know if a previous message was recieved correctly
		// and provides us with the resulting error if not
		case Completion:
			s.logger.Infof("trapped on completion?")
			if err := s.processCompletionMessage(rawMessage); err != nil {
				s.logger.Error(err)
			}

		// These messages are regular SignalR messages that we'll process and
		// forward to whoever is listening
		case Invocation:
			s.logger.Infof("trapped on invocation?")
			var message SignalRMessage
			if err := json.Unmarshal(rawMessage, &message); err != nil {
				return fmt.Errorf("error unmarshalling SignalR message: %s. Error: %w", string(rawMessage), err)
			}

			// Push message to whoever's listening
			s.inbound <- &message

		default:
			s.logger.Infof("Ignoring %s message", SignalRMessageType(signalRMessageType.Type).String())
		}
	}

	return nil
}

func (s *SignalR) processCompletionMessage(msg []byte) error {
	var completionMessage CompletionMessage
	if err := json.Unmarshal(msg, &completionMessage); err != nil {
		return fmt.Errorf("error unmarshalling SignalR completion message: %s", string(msg))
	}

	// A completion message is only valuable as long as it's referring to an existing, sent message
	if completionMessage.InvocationId == nil {
		return fmt.Errorf("received completion message without an invocationId: %s", string(msg))
	}

	invocationId := *completionMessage.InvocationId
	message, ok := s.invocator.Match(invocationId)
	if !ok {
		return fmt.Errorf("received completion message for a message we did not send")
	}

	// Check if our completion message is trying to let us know an error happened on the server while
	// processing the message
	if completionMessage.Error != nil {
		return fmt.Errorf("server error on message type %s: %s", message.MessageType, *completionMessage.Error)
	} else if completionMessage.Result != nil && completionMessage.Result.Error {
		return fmt.Errorf("server error on message type %s: %s", message.MessageType, *completionMessage.Result.ErrorMessage)
	}

	return nil
}

func (s *SignalR) wrap(message am.AgentMessage) error {
	// Select SignalR Endpoint
	target, err := s.targetSelector(message)
	if err != nil {
		return fmt.Errorf("error in selecting SignalR Endpoint target name: %w", err)
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal agent message: %w", err)
	}

	invocationId := s.invocator.GetInvocationId()

	wrappedMessage := SignalRMessage{
		Target:       target,
		Type:         int(Invocation),
		Arguments:    []json.RawMessage{messageBytes},
		InvocationId: &invocationId,
	}

	msgBytes, _ := json.Marshal(wrappedMessage)
	if err != nil {
		return fmt.Errorf("error marshalling outgoing SignalR Message: %+v", wrappedMessage)
	}

	// Write our message to our connection
	err = s.client.Send(msgBytes)

	// Only track the message once we're absolutely sure it's been sent off
	// this protects our invocator from tracking messages it will never receive
	// a response for
	if err != nil {
		s.invocator.Track(invocationId, message)
	}

	return err
}

func buildUrl(serviceUrl string, params map[string][]string) (*url.URL, error) {
	// Build our websocket url object
	websocketUrl, err := url.Parse(serviceUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection node service url %s: %w", serviceUrl, err)
	}

	// Set our params as encoded args
	urlParams := url.Values(params)
	websocketUrl.RawQuery = urlParams.Encode()

	return websocketUrl, nil
}
