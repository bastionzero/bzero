package exec

import (
	"bastionzero.com/bctl/v1/bzerolib/plugin/kube/actions/exec"
	"k8s.io/client-go/tools/remotecommand"
)

type TerminalSizeQueue struct {
	execResizeChannel chan exec.KubeExecResizeActionPayload
	RequestId         string
}

func NewTerminalSizeQueue(requestId string, execResizeChannel chan exec.KubeExecResizeActionPayload) *TerminalSizeQueue {
	return &TerminalSizeQueue{
		execResizeChannel: execResizeChannel,
		RequestId:         requestId,
	}
}

func (t *TerminalSizeQueue) Next() *remotecommand.TerminalSize {
	tsMessage := <-t.execResizeChannel

	return &remotecommand.TerminalSize{
		Width:  tsMessage.Width,
		Height: tsMessage.Height,
	}
}
