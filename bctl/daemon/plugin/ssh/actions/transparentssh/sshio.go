package transparentssh

import (
	"bytes"
	"encoding/json"
	"io"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin"
	"bastionzero.com/bctl/v1/bzerolib/plugin/ssh"
)

type StdReader struct {
	logger        *logger.Logger
	outputChannel chan plugin.ActionWrapper
	Action        string
}

// Stdout or Stderr
func NewStdReader(logger *logger.Logger, ch chan plugin.ActionWrapper, streamAction string) *StdReader {
	return &StdReader{
		logger:        logger,
		outputChannel: ch,
		Action:        streamAction,
	}
}

func (w *StdReader) Read(p []byte) (int, error) {
	sshInputDataMessage := ssh.SshInputMessage{
		Data: p,
	}

	w.logger.Infof("I read %d bytes: %s", len(p), p)
	payloadBytes, _ := json.Marshal(sshInputDataMessage)
	message := plugin.ActionWrapper{
		Action:        string(w.Action),
		ActionPayload: payloadBytes,
	}
	w.outputChannel <- message

	return len(p), nil

}

var (
	EndStreamBytes = []byte{0x62, 0x61, 0x73, 0x74, 0x69, 0x6f, 0x6e, 0x7a, 0x65, 0x72, 0x6f} // "BastionZero"
)

// Stdin
type StdWriter struct {
	logger       *logger.Logger
	stdinChannel chan []byte
	doneChannel  chan bool
}

func NewStdWriter(logger *logger.Logger, stdinChannel chan []byte) *StdWriter {
	logger.Infof("I'm stdWriter and I exist...")
	stdin := &StdWriter{
		logger:       logger,
		stdinChannel: stdinChannel,
		doneChannel:  make(chan bool),
	}

	return stdin
}

func (r *StdWriter) Close() {
	r.doneChannel <- true
}

func (r *StdWriter) Write(p []byte) (int, error) {
	r.logger.Infof("I was asked to write, right?")
	// Listen for data on our stdinChannel
	if bytes.Equal(p, EndStreamBytes) {
		return 1, io.EOF
	}
	select {
	case stdin := <-r.stdinChannel:

		r.logger.Infof("I'm writing %s", stdin)
		n := copy(p, stdin)
		return n, nil
	case <-r.doneChannel:
		return 1, io.EOF
	}
}
