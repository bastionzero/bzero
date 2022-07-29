package websocket

import (
	"fmt"
	"net/http"
	"net/url"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	gorilla "github.com/gorilla/websocket"
)

type IWebsocket interface {
	Done() <-chan struct{}
	Inbound() <-chan *[]byte
	Dial(endpoint string, params map[string][]string) error
	Send(message []byte) error
}

type Websocket struct {
	logger   *logger.Logger
	doneChan chan struct{}
	isDead   bool

	client *gorilla.Conn

	// Received messages
	inbound chan *[]byte

	// Messages to be sent
	outbound chan *[]byte
}

func New(logger *logger.Logger) *Websocket {
	ws := &Websocket{
		doneChan: make(chan struct{}),
		logger:   logger,
		inbound:  make(chan *[]byte, 200),
		outbound: make(chan *[]byte, 200),
	}

	go func() {
		<-ws.doneChan
		ws.logger.Infof("chan definitely done 1")
	}()

	go func() {
		<-ws.doneChan
		ws.logger.Infof("chan definitely done 2")
	}()

	return ws
}

func (w *Websocket) Close() {
	if !w.isDead {
		w.isDead = true
		close(w.doneChan)
	}
}

func (w *Websocket) Done() <-chan struct{} {
	w.logger.Info("SOMEONE IS LISTENING")
	return w.doneChan
}

func (w *Websocket) Inbound() <-chan *[]byte {
	return w.inbound
}

func (w *Websocket) Send(message []byte) error {
	if err := w.client.WriteMessage(gorilla.TextMessage, message); err != nil {
		return err
	}
	return nil
}

func (w *Websocket) Dial(websocketUrl *url.URL) (err error) {
	// Reinitialize our variables every time in case this is post death
	// LUCIE: race condition between done() and dial reconnecting after death?
	if w.isDead {
		w.logger.Infof("WE MADE ANOTHER CHANNEL")
		w.doneChan = make(chan struct{})
		w.isDead = false
	}

	// Make sure url scheme is correct
	websocketUrl.Scheme = "wss"

	// Try to connect websocket once
	if w.client, _, err = gorilla.DefaultDialer.Dial(websocketUrl.String(), http.Header{}); err != nil {
		return fmt.Errorf("error dialing websocket: %s", err)
	}

	go w.receive()

	// Send outgoing messages
	go func() {
		defer w.logger.Info("Websocket connection closed")

		for {
			select {
			case <-w.doneChan:
				return
			case msg := <-w.outbound:
				if err := w.Send(*msg); err != nil {
					w.logger.Error(err)
				}
			}
		}
	}()

	return nil
}

func (w *Websocket) receive() {
	defer func() {
		if !w.isDead {
			close(w.doneChan)
		}
		w.isDead = true
	}()

	for {
		// Read incoming message(s)
		if _, rawMessage, err := w.client.ReadMessage(); w.isDead {
			w.client.Close()
		} else if err != nil {
			// Check if it's a clean exit
			if !gorilla.IsCloseError(err, gorilla.CloseNormalClosure) {
				w.logger.Error(err)
			}
			return
		} else {
			w.inbound <- &rawMessage
		}
	}
}
