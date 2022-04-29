package web

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

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
	Start(writer http.ResponseWriter, request *http.Request) error
	Kill()
}

type WebDaemonPlugin struct {
	logger   *logger.Logger
	action   IWebDaemonAction
	doneChan chan struct{}
	killed   bool

	// keysplitting output
	outputQueue chan plugin.ActionWrapper

	// Web-specific vars
	remoteHost string
	remotePort int

	// For processing incoming messages in order
	sequenceNumber int
}

func New(logger *logger.Logger, remoteHost string, remotePort int) *WebDaemonPlugin {
	return &WebDaemonPlugin{
		logger:         logger,
		doneChan:       make(chan struct{}),
		killed:         false,
		outputQueue:    make(chan plugin.ActionWrapper, 5),
		remoteHost:     remoteHost,
		remotePort:     remotePort,
		sequenceNumber: 0,
	}
}

func (w *WebDaemonPlugin) StartAction(action bzweb.WebAction, writer http.ResponseWriter, request *http.Request) error {
	if w.killed {
		return fmt.Errorf("plugin has already been killed, cannot create a new web action")
	}

	// Always generate a requestId, each new web command is its own request
	requestId := uuid.New().String()

	// Create action logger
	actLogger := w.logger.GetActionLogger(string(action))

	switch action {
	case bzweb.Dial:
		w.action = webdial.New(actLogger, requestId, w.outputQueue, w.doneChan)
	case bzweb.Websocket:
		w.action = webwebsocket.New(actLogger, requestId, w.outputQueue, w.doneChan)
	default:
		rerr := fmt.Errorf("unrecognized web action: %s", action)
		w.logger.Error(rerr)
		return rerr
	}

	w.logger.Infof("Web plugin created a %s action", action)

	// send local tcp connection to action
	if err := w.action.Start(writer, request); err != nil {
		return err
	}

	return nil
}

func (w *WebDaemonPlugin) Outbox() <-chan plugin.ActionWrapper {
	return w.outputQueue
}

func (w *WebDaemonPlugin) Done() <-chan struct{} {
	return w.doneChan
}

func (w *WebDaemonPlugin) Kill() {
	w.killed = true
	if w.action != nil {
		w.action.Kill()
	}
}

func (w *WebDaemonPlugin) ReceiveStream(smessage smsg.StreamMessage) {
	// w.logger.Debugf("Web received %v", smessage.Type)
	if w.action != nil {
		w.action.ReceiveStream(smessage)
	} else {
		w.logger.Errorf("web plugin received stream message before an action was created. Ignoring")
	}
}

func (w *WebDaemonPlugin) ReceiveKeysplitting(action string, actionPayload []byte) error {
	w.logger.Debugf("Received %s keysplitting message", action)

	// the only keysplitting message that we would receive is the ack for our web action interrupt
	// we don't do anything with it on the daemon side, so we receive it here and it will get logged
	// but no particular action will be taken
	return nil
}
