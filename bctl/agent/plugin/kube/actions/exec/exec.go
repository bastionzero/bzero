package exec

import (
	"encoding/json"
	"fmt"
	"net/url"

	"gopkg.in/tomb.v2"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	bzexec "bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/exec"
	kubeutils "bastionzero.com/bctl/v1/bzerolib/plugin/kube/utils"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

// wrap this code so at test time we can inject a mock executor / config
var getExecutor = func(config *rest.Config, method string, url *url.URL) (remotecommand.Executor, error) {
	return remotecommand.NewSPDYExecutor(config, method, url)
}

var getConfig = func() (*rest.Config, error) {
	return rest.InClusterConfig()
}

type ExecAction struct {
	logger *logger.Logger
	tmb    *tomb.Tomb
	closed bool

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan     chan smsg.StreamMessage
	streamMessageVersion smsg.SchemaVersion

	// To send input/resize to our exec sessions
	execStdinChannel  chan []byte
	execResizeChannel chan bzexec.KubeExecResizeActionPayload

	serviceAccountToken string
	kubeHost            string
	targetGroups        []string
	targetUser          string
	logId               string
	requestId           string
}

func New(logger *logger.Logger,
	pluginTmb *tomb.Tomb,
	serviceAccountToken string,
	kubeHost string,
	targetGroups []string,
	targetUser string,
	ch chan smsg.StreamMessage) (*ExecAction, error) {

	return &ExecAction{
		logger:              logger,
		tmb:                 pluginTmb,
		closed:              false,
		streamOutputChan:    ch,
		execStdinChannel:    make(chan []byte, 10),
		execResizeChannel:   make(chan bzexec.KubeExecResizeActionPayload, 10),
		serviceAccountToken: serviceAccountToken,
		kubeHost:            kubeHost,
		targetGroups:        targetGroups,
		targetUser:          targetUser,
	}, nil
}

func (e *ExecAction) Closed() bool {
	return e.closed
}

func (e *ExecAction) Receive(action string, actionPayload []byte) (string, []byte, error) {
	switch bzexec.ExecSubAction(action) {

	// Start exec message required before anything else
	case bzexec.ExecStart:
		var startExecRequest bzexec.KubeExecStartActionPayload
		if err := json.Unmarshal(actionPayload, &startExecRequest); err != nil {
			rerr := fmt.Errorf("unable to unmarshal start exec message: %s", err)
			e.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		return e.StartExec(startExecRequest)

	case bzexec.ExecInput:
		var execInputAction bzexec.KubeStdinActionPayload
		if err := json.Unmarshal(actionPayload, &execInputAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling stdin: %s", err)
			e.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		// Always feed in the exec stdin a chunk at a time (i.e. break up the byte array into chunks)
		for i := 0; i < len(execInputAction.Stdin); i += kubeutils.ExecChunkSize {
			end := i + kubeutils.ExecChunkSize
			if end > len(execInputAction.Stdin) {
				end = len(execInputAction.Stdin)
			}
			e.execStdinChannel <- execInputAction.Stdin[i:end]
		}
		return string(bzexec.ExecInput), []byte{}, nil

	case bzexec.ExecResize:
		var execResizeAction bzexec.KubeExecResizeActionPayload
		if err := json.Unmarshal(actionPayload, &execResizeAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling resize message: %s", err)
			e.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		e.execResizeChannel <- execResizeAction
		return string(bzexec.ExecResize), []byte{}, nil
	case bzexec.ExecStop:
		var execStopAction bzexec.KubeExecStopActionPayload
		if err := json.Unmarshal(actionPayload, &execStopAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling stop message: %s", err)
			e.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		e.closed = true
		return string(bzexec.ExecStop), []byte{}, nil
	default:
		rerr := fmt.Errorf("unhandled exec action: %v", action)
		e.logger.Error(rerr)
		return "", []byte{}, rerr
	}
}

func (e *ExecAction) StartExec(startExecRequest bzexec.KubeExecStartActionPayload) (string, []byte, error) {
	// keep track of who we're talking to
	e.requestId = startExecRequest.RequestId
	e.logger.Infof("Setting request id: %s", e.requestId)
	e.logId = startExecRequest.LogId
	e.streamMessageVersion = startExecRequest.StreamMessageVersion
	e.logger.Infof("Setting stream message version: %s", e.streamMessageVersion)

	// Now open up our local exec session
	// Create the in-cluster config
	config, err := getConfig()
	if err != nil {
		rerr := fmt.Errorf("error creating in-custer config: %s", err)
		e.logger.Error(rerr)
		return "", []byte{}, rerr
	}

	// Always ensure that our targetUser is set
	if e.targetUser == "" {
		rerr := fmt.Errorf("target user field is not set")
		e.logger.Error(rerr)
		return "", []byte{}, rerr
	}

	// Add our impersonation information
	config.Impersonate = rest.ImpersonationConfig{
		UserName: e.targetUser,
		Groups:   e.targetGroups,
	}
	config.BearerToken = e.serviceAccountToken

	kubeExecApiUrl := e.kubeHost + startExecRequest.Endpoint
	kubeExecApiUrlParsed, err := url.Parse(kubeExecApiUrl)
	if err != nil {
		rerr := fmt.Errorf("could not parse kube exec url: %s", err)
		e.logger.Error(rerr)
		return "", []byte{}, rerr
	}

	// Turn it into a SPDY executor
	exec, err := getExecutor(config, "POST", kubeExecApiUrlParsed)
	if err != nil {
		return string(bzexec.ExecStart), []byte{}, fmt.Errorf("error creating Spdy executor: %s", err)
	}

	// NOTE: don't need to version this because Type is not read on the other end
	stderrWriter := NewStdWriter(e.streamOutputChan, e.streamMessageVersion, e.requestId, string(kube.Exec), smsg.StdErr, e.logId)
	stdoutWriter := NewStdWriter(e.streamOutputChan, e.streamMessageVersion, e.requestId, string(kube.Exec), smsg.StdOut, e.logId)
	stdinReader := NewStdReader(string(bzexec.StdIn), startExecRequest.RequestId, e.execStdinChannel)
	terminalSizeQueue := NewTerminalSizeQueue(startExecRequest.RequestId, e.execResizeChannel)

	// This function listens for a closed datachannel.  If the datachannel is closed, it doesn't necessarily mean
	// that the exec was properly closed, and because the below exec.Stream only returns when it's done, there's
	// no way to interrupt it or pass in a context. Therefore, we need to close the stream in order to pass an
	// io.EOF message to exec which will close the exec.Stream and that will close the go routine.
	// https://github.com/kubernetes/client-go/issues/554
	go func() {
		<-e.tmb.Dying()
		stdinReader.Close()
	}()

	// runs the exec interaction with the kube server
	go func() {
		if startExecRequest.IsTty {
			err = exec.Stream(remotecommand.StreamOptions{
				Stdin:             stdinReader,
				Stdout:            stdoutWriter,
				Stderr:            stderrWriter,
				TerminalSizeQueue: terminalSizeQueue,
				Tty:               true,
			})
		} else {
			err = exec.Stream(remotecommand.StreamOptions{
				Stdin:  stdinReader,
				Stdout: stdoutWriter,
				Stderr: stderrWriter,
			})
		}

		if err != nil {
			// Log the error
			rerr := fmt.Errorf("error in SPDY stream: %s", err)
			e.logger.Error(rerr)

			// Also write the error to our stdoutWriter so the user can see it
			stdoutWriter.Write([]byte(fmt.Sprint(err)))
		}

		// Now close the stream
		stdoutWriter.Write([]byte(bzexec.EscChar))

		e.closed = true
	}()

	return string(bzexec.ExecStart), []byte{}, nil
}
