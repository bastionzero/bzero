package web

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/plugin/web/actions/webdial"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/web/actions/webwebsocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	bzweb "bastionzero.com/bctl/v1/bzerolib/plugin/web"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// Perhaps unnecessary but it is nice to make sure that each action is implementing a common function set
type IWebDaemonAction interface {
	ReceiveStream(stream smsg.StreamMessage)
	Start(tmb *tomb.Tomb, Writer http.ResponseWriter, Request *http.Request) error
	Done() <-chan struct{}
}

type WebDaemonPlugin struct {
	tmb    *tomb.Tomb
	logger *logger.Logger

	// Input and output channels
	streamInputChan chan smsg.StreamMessage
	outputQueue     chan plugin.ActionWrapper

	// Channel for letting the datachannel know we're done
	doneChan chan struct{}

	action IWebDaemonAction

	// Web-specific vars
	remoteHost string
	remotePort int

	// For processing incoming messages in order
	sequenceNumber int
}

func New(parentTmb *tomb.Tomb,
	logger *logger.Logger,
	actionParams bzweb.WebActionParams) (*WebDaemonPlugin, error) {
	plugin := WebDaemonPlugin{
		tmb:    parentTmb,
		logger: logger,

		streamInputChan: make(chan smsg.StreamMessage, 25),
		outputQueue:     make(chan plugin.ActionWrapper),

		doneChan: make(chan struct{}),

		remoteHost: actionParams.RemoteHost,
		remotePort: actionParams.RemotePort,

		sequenceNumber: 0,
	}

	// listener for processing any incoming stream messages, since they are not treated as part of
	// the keysplitting synchronous chain
	go func() {
		for {
			select {
			case <-plugin.tmb.Dying():
				return
			case streamMessage := <-plugin.streamInputChan:
				plugin.processStream(streamMessage)
			}
		}
	}()

	return &plugin, nil
}

func (w *WebDaemonPlugin) Outbox() <-chan plugin.ActionWrapper {
	return w.outputQueue
}

func (w *WebDaemonPlugin) Done() <-chan struct{} {
	return w.doneChan
}

func (w *WebDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	w.logger.Debugf("Web dial action received %v stream", smessage.Type)
	w.streamInputChan <- smessage
}

func (w *WebDaemonPlugin) processStream(smessage smsg.StreamMessage) error {
	if w.action != nil {
		w.action.ReceiveStream(smessage)
		return nil
	} else {
		return fmt.Errorf("web plugin received stream message before an action was created. Ignoring")
	}
}

func (w *WebDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) error {
	w.logger.Debugf("Received a keysplitting message with action: %s", action)
	// the only keysplitting message that we would receive is the ack for our web action interrupt
	// we don't do anything with it on the daemon side, so we receive it here and it will get logged
	// but no particular action will be taken
	return nil
}

func (w *WebDaemonPlugin) Feed(food interface{}) error {
	// Make sure food matches what it says on the label
	webFood, ok := food.(bzweb.WebFood)
	if !ok {
		return fmt.Errorf("web plugin's food did not match nutrition label: %+v", webFood)
	}
	// Always generate a requestId, each new web command is its own request
	requestId := uuid.New().String()

	// Create action logger
	actLogger := w.logger.GetActionLogger(string(webFood.Action))

	switch webFood.Action {
	case bzweb.Dial:
		w.action = webdial.New(actLogger, requestId, w.outputQueue)
	case bzweb.Websocket:
		w.action = webwebsocket.New(actLogger, requestId, w.outputQueue)
	default:
		rerr := fmt.Errorf("unrecognized web action: %v", string(webFood.Action))
		w.logger.Error(rerr)
		return rerr
	}

	go func() {
		select {
		case <-w.tmb.Dying():
			return
		case <-w.action.Done():
			// if the action is done, then so are we
			close(w.doneChan)
			return
		}
	}()

	w.logger.Infof("Web plugin created a %s action", string(webFood.Action))

	// send local tcp connection to action
	if err := w.action.Start(w.tmb, webFood.Writer, webFood.Request); err != nil {
		w.logger.Error(fmt.Errorf("%s error: %s", string(webFood.Action), err))
	}

	return nil
}
