package utils

import "time"

// Some of the code here is taken from teleports open source repo (Apache 2.0)
// which can be found here:
// * https://github.com/gravitational/teleport/blob/740d184d1cfc69ae2e96c50ee738b13884fb232b/lib/kube/proxy/constants.go

const (
	// Default timeout for streams
	DefaultStreamCreationTimeout = 30 * time.Second

	// Default idle timeout for conn
	DefaultIdleTimeout = 4 * time.Hour

	// Enable stdin for remote command execution
	ExecStdinParam = "stdin"
	// Enable stdout for remote command execution
	ExecStdoutParam = "stdout"
	// Enable stderr for remote command execution
	ExecStderrParam = "stderr"
	// Enable TTY for remote command execution
	ExecTTYParam = "tty"
	// Command to run for remote command execution
	ExecCommandParam = "command"

	// Name of header that specifies stream type
	StreamType = "StreamType"
	// Value for streamType header for stdin stream
	StreamTypeStdin = "stdin"
	// Value for streamType header for stdout stream
	StreamTypeStdout = "stdout"
	// Value for streamType header for stderr stream
	StreamTypeStderr = "stderr"
	// Value for streamType header for data stream
	StreamTypeData = "data"
	// Value for streamType header for error stream
	StreamTypeError = "error"
	// Value for streamType header for terminal resize stream
	StreamTypeResize = "resize"

	// Name of header that specifies the port being forwarded
	PortHeader = "Port"
	// Name of header that specifies a request ID used to associate the error
	// and data streams for a single forwarded connection
	PortForwardRequestIDHeader = "RequestId"
	// PortForwardProtocolV1Name is the subprotocol "portforward.k8s.io" is used for port forwarding
	PortForwardProtocolV1Name = "portforward.k8s.io"

	// HighResPollingPeriod is a default high resolution polling period
	HighResPollingPeriod = 10 * time.Second

	// Chunk size that exec is expecting
	ExecChunkSize = 8192

	// Default max buffer size for exec to send over the wire
	ExecDefaultMaxBufferSize = 1024 * 1024 * 64
)
