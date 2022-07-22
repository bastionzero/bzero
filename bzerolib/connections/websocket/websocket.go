package websocket

import (
	"fmt"
	"net/http"
	"net/url"

	gorilla "github.com/gorilla/websocket"
	"gopkg.in/tomb.v2"
)

type IWebsocket interface {
	Close()
	Dial()
	Reconnect()
	Inbox()
	Send()
}

type Websocket struct {
	tmb      tomb.Tomb
	doneChan chan struct{}

	// Received messages
	inbound chan *[]byte

	// Messages to be sent
	outbound chan *[]byte

	client *gorilla.Conn
}

func New() *Websocket {
	return &Websocket{
		inbound:  make(chan *[]byte, 200),
		outbound: make(chan *[]byte, 200),
	}
}

func (w *Websocket) Close() {
	if w.tmb.Alive() {
		w.tmb.Kill(nil)
		w.tmb.Wait()
	}
}

func (w *Websocket) Done() <-chan struct{} {
	return w.doneChan
}

func (w *Websocket) Inbound() <-chan *[]byte {
	return w.inbound
}

func (w *Websocket) Dial(endpoint string, params map[string][]string) error {
	// Build websocket URL
	websocketUrl, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("error parsing url %s: %w", endpoint, err)
	}
	websocketUrl.Scheme = "wss"
	query := url.Values(params)
	websocketUrl.RawQuery = query.Encode()

	// Try to connect websocket once
	if w.client, _, err = gorilla.DefaultDialer.Dial(websocketUrl.String(), http.Header{}); err != nil {
		return fmt.Errorf("error dialing websocket: %w", err)
	}

	// Set our go routines for listening and writing to our websocket connection
	w.tmb.Go(func() error {
		defer close(w.doneChan)
		defer w.client.Close()

		// Listen for incoming messages
		w.tmb.Go(w.receive)

		// Send outgoing messages
		for {
			select {
			case <-w.tmb.Dying():
				return nil
			case msg := <-w.outbound:
				if err := w.Send(*msg); err != nil {
					return err
				}
			}
		}
	})

	return nil
}

func (w *Websocket) receive() error {
	for {
		// Read incoming message(s)
		if _, rawMessage, err := w.client.ReadMessage(); !w.tmb.Alive() {
			return nil
		} else if err != nil {

			// Check if it's a clean exit
			if gorilla.IsCloseError(err, gorilla.CloseNormalClosure) {
				return nil
			}
			return fmt.Errorf("abnormal websocket closure: %w", err)

		} else {
			w.inbound <- &rawMessage
		}
	}
}

func (w *Websocket) Send(message []byte) error {
	if err := w.client.WriteMessage(gorilla.TextMessage, message); err != nil {
		return err
	}
	return nil
}
