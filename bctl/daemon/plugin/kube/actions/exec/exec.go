package exec

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/exec"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type ExecAction struct {
	logger *logger.Logger

	requestId       string
	logId           string
	commandBeingRun string

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage
	ksInputChan     chan plugin.ActionWrapper
}

func New(logger *logger.Logger,
	requestId string,
	logId string,
	commandBeingRun string) (*ExecAction, chan plugin.ActionWrapper) {

	exec := &ExecAction{
		logger:          logger,
		requestId:       requestId,
		logId:           logId,
		commandBeingRun: commandBeingRun,
		outputChan:      make(chan plugin.ActionWrapper, 10),
		streamInputChan: make(chan smsg.StreamMessage, 10),
		ksInputChan:     make(chan plugin.ActionWrapper, 10),
	}

	return exec, exec.outputChan
}

func (e *ExecAction) ReceiveKeysplitting(wrappedAction plugin.ActionWrapper) {
	e.ksInputChan <- wrappedAction
}

func (e *ExecAction) ReceiveStream(stream smsg.StreamMessage) {
	e.streamInputChan <- stream
}

func (e *ExecAction) Start(tmb *tomb.Tomb, writer http.ResponseWriter, request *http.Request) error {
	// create new SPDY service for exec communication
	subLogger := e.logger.GetComponentLogger("SPDY")
	spdy, err := NewSPDYService(subLogger, writer, request)
	if err != nil {
		e.logger.Error(err)
		return err
	}

	// Determine if this is tty
	isTty := kubeutils.IsQueryParamPresent(request, "tty")

	// Now since we made our local connection to kubectl, initiate a connection with Bastion
	// FIXME: just put this in a class function...
	e.sendStartMessage(isTty, request.URL.Query()["command"], request.URL.String())

	// Set up a go function for stdout
	go func() {
		defer close(e.outputChan)
		streamQueue := make(map[int]smsg.StreamMessage)
		seqNumber := 0

		for {
			select {
			case <-tmb.Dying():
				return
			case streamMessage := <-e.streamInputChan:
				// check if received message is out of order
				if streamMessage.SequenceNumber != seqNumber {
					streamQueue[streamMessage.SequenceNumber] = streamMessage
				} else {
					// process in-order message + any next messages that we already received
					msg := streamMessage
					ok := true
					for ok {

						// check for end of stream
						contentBytes, _ := base64.StdEncoding.DecodeString(msg.Content)
						if string(contentBytes) == exec.EscChar {
							e.logger.Info("exec stream ended")
							spdy.conn.Close()
							break
						}

						// write message to output
						spdy.stdoutStream.Write(contentBytes)

						// delete processed message, increment sequence number and grab next (if there is one)
						delete(streamQueue, seqNumber)
						seqNumber++
						msg, ok = streamQueue[seqNumber]
					}
				}
			}
		}
	}()

	// Set up a go function for stdin
	go func() {

		for {
			// Reset buffer every loop
			buffer := make([]byte, 0)

			// Define our chunkBuffer
			chunkSizeBuffer := make([]byte, kubeutils.ExecChunkSize)

			select {
			case <-tmb.Dying():
				return
			default:
				// Keep reading from our stdin stream if we see multiple chunks coming in
				for {
					n, err := spdy.stdinStream.Read(chunkSizeBuffer)

					// Always return if we see a EOF
					if err == io.EOF {
						return
					}

					// Append the new chunk to our buffer
					buffer = append(buffer, chunkSizeBuffer[:n]...)

					// If we stop seeing chunks (i.e. n != 8192) or we have reached our max buffer size, break
					if n != kubeutils.ExecChunkSize || len(buffer) > kubeutils.ExecDefaultMaxBufferSize {
						break
					}
				}

				// Send message to agent
				e.sendStdinPayload(buffer)
			}
		}

	}()

	if isTty {
		// Set up a go function for resize if we are running interactively
		go func() {
			for {
				select {
				case <-tmb.Dying():
					return
				default:
					decoder := json.NewDecoder(spdy.resizeStream)

					size := TerminalSize{}
					if err := decoder.Decode(&size); err != nil {
						if err == io.EOF {
							return
						} else {
							e.logger.Error(fmt.Errorf("error decoding resize message: %s", err))
						}
					} else {
						// Emit this as a new resize event
						e.sendResizeMessage(size.Width, size.Height)
					}
				}
			}
		}()
	}

	closeChan := spdy.conn.CloseChan()

	go func() {
		for {
			select {
			case <-tmb.Dying():
				return
			case <-closeChan:
				// Send message to agent to close the stream
				payload := exec.KubeExecStopActionPayload{
					RequestId: e.requestId,
					LogId:     e.logId,
				}

				payloadBytes, _ := json.Marshal(payload)
				e.outputChan <- plugin.ActionWrapper{
					Action:        string(exec.ExecStop),
					ActionPayload: payloadBytes,
				}

				return
			}
		}
	}()

	return nil
}

func (e *ExecAction) sendStartMessage(isTty bool, command []string, endpoint string) {
	payload := exec.KubeExecStartActionPayload{
		RequestId:            e.requestId,
		StreamMessageVersion: smsg.CurrentSchema,
		LogId:                e.logId,
		IsTty:                isTty,
		Command:              command,
		Endpoint:             endpoint,
		CommandBeingRun:      e.commandBeingRun,
	}

	payloadBytes, _ := json.Marshal(payload)
	e.outputChan <- plugin.ActionWrapper{
		Action:        string(exec.ExecStart),
		ActionPayload: payloadBytes,
	}
}

func (e *ExecAction) sendResizeMessage(width uint16, height uint16) {
	payload := exec.KubeExecResizeActionPayload{
		RequestId: e.requestId,
		LogId:     e.logId,
		Width:     width,
		Height:    height,
	}

	payloadBytes, _ := json.Marshal(payload)
	e.outputChan <- plugin.ActionWrapper{
		Action:        string(exec.ExecResize),
		ActionPayload: payloadBytes,
	}
}

func (e *ExecAction) sendStdinPayload(stdin []byte) {
	payload := exec.KubeStdinActionPayload{
		RequestId: e.requestId,
		LogId:     e.logId,
		Stdin:     stdin,
	}

	payloadBytes, _ := json.Marshal(payload)
	e.outputChan <- plugin.ActionWrapper{
		Action:        string(exec.ExecInput),
		ActionPayload: payloadBytes,
	}
}
