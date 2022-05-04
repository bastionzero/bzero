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
	tmb    tomb.Tomb
	logger *logger.Logger

	requestId       string
	logId           string
	commandBeingRun string
	doneChan        chan struct{}

	// input and output channels relative to this plugin
	outputChan      chan plugin.ActionWrapper
	streamInputChan chan smsg.StreamMessage
}

func New(
	logger *logger.Logger,
	outputChan chan plugin.ActionWrapper,
	doneChan chan struct{},
	requestId string,
	logId string,
	commandBeingRun string,
) *ExecAction {

	return &ExecAction{
		logger:          logger,
		requestId:       requestId,
		logId:           logId,
		commandBeingRun: commandBeingRun,
		doneChan:        doneChan,
		outputChan:      outputChan,
		streamInputChan: make(chan smsg.StreamMessage, 10),
	}
}

func (e *ExecAction) Kill() {
	e.tmb.Kill(nil)
	e.tmb.Wait()
}

func (e *ExecAction) ReceiveKeysplitting(actionPayload []byte) {}

func (e *ExecAction) ReceiveStream(stream smsg.StreamMessage) {
	e.streamInputChan <- stream
}

func (e *ExecAction) Start(writer http.ResponseWriter, request *http.Request) error {
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
	e.sendStartPayload(isTty, e.requestId, e.logId, request.URL.Query()["command"], request.URL.String())

	// Set up a go function for stdout
	e.tmb.Go(func() error {
		defer close(e.doneChan)

		streamQueue := make(map[int]smsg.StreamMessage)
		seqNumber := 0
		closeChan := spdy.conn.CloseChan()

		for {
			select {
			case <-e.tmb.Dying():
				return nil
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
							return nil
						}

						// write message to output
						spdy.stdoutStream.Write(contentBytes)

						// delete processed message, increment sequence number and grab next (if there is one)
						delete(streamQueue, seqNumber)
						seqNumber++
						msg, ok = streamQueue[seqNumber]
					}
				}
			case <-closeChan:
				// Send message to agent to close the stream
				payload := exec.KubeExecStopActionPayload{
					RequestId: e.requestId,
					LogId:     e.logId,
				}
				e.outbox(exec.ExecStop, payload)
				return nil
			}
		}
	})

	// Set up a go function to read from stdin
	go func() {
		for {
			// Reset buffer every loop
			buffer := make([]byte, 0)

			// Define our chunkBuffer
			chunkSizeBuffer := make([]byte, kubeutils.ExecChunkSize)

			select {
			case <-e.tmb.Dying():
				return
			default:
				// Keep reading from our stdin stream if we see multiple chunks coming in
				for {
					if n, err := spdy.stdinStream.Read(chunkSizeBuffer); !e.tmb.Alive() {
						return
					} else if err == io.EOF {
						buffer = append(buffer, chunkSizeBuffer[:n]...)
						// Always return if we see a EOF
						break
					} else if err != nil {
						e.logger.Errorf("failed reading stdin: %s", err)
					} else {
						// Append the new chunk to our buffer
						buffer = append(buffer, chunkSizeBuffer[:n]...)

						// If we stop seeing chunks (i.e. n != 8192) or we have reached our max buffer size, break
						if n != kubeutils.ExecChunkSize || len(buffer) > kubeutils.ExecDefaultMaxBufferSize {
							break
						}
					}
				}
				// Send message to agent
				e.sendStdinPayload(e.requestId, e.logId, buffer)
			}
		}
	}()

	if isTty {
		// Set up a go function for resize if we are running interactively
		go func() {
			for {
				select {
				case <-e.tmb.Dying():
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
						e.sendResizePayload(e.requestId, e.logId, size.Width, size.Height)
					}
				}
			}
		}()
	}

	return nil
}

func (e *ExecAction) outbox(action exec.ExecSubAction, payload interface{}) {
	// Send payload to plugin output queue
	payloadBytes, _ := json.Marshal(payload)
	e.outputChan <- plugin.ActionWrapper{
		Action:        string(action),
		ActionPayload: &payloadBytes,
	}
}

func (e *ExecAction) sendStartPayload(isTty bool, requestId string, logId string, command []string, endpoint string) {
	payload := exec.KubeExecStartActionPayload{
		RequestId:            requestId,
		StreamMessageVersion: smsg.CurrentSchema,
		LogId:                logId,
		IsTty:                isTty,
		Command:              command,
		Endpoint:             endpoint,
	}
	e.outbox(exec.ExecStart, payload)
}

func (e *ExecAction) sendResizePayload(requestId string, logId string, width uint16, height uint16) {
	payload := exec.KubeExecResizeActionPayload{
		RequestId: requestId,
		LogId:     logId,
		Width:     width,
		Height:    height,
	}
	e.outbox(exec.ExecResize, payload)
}

func (e *ExecAction) sendStdinPayload(requestId string, logId string, stdin []byte) {
	payload := exec.KubeStdinActionPayload{
		RequestId: requestId,
		LogId:     logId,
		Stdin:     stdin,
	}
	e.outbox(exec.ExecInput, payload)
}
