package exec

type ExecSubAction string

const (
	ExecStart  ExecSubAction = "kube/exec/start"
	ExecInput  ExecSubAction = "kube/exec/input"
	ExecResize ExecSubAction = "kube/exec/resize"
	ExecStop   ExecSubAction = "kube/exec/stop"
	StdIn      ExecSubAction = "kube/exec/stdin"
)

const (
	EscChar = "^[" // ESC char
)
