package bzos

import (
	"os"
	"os/signal"
	"syscall"
)

// ref: https://github.com/bastionzero/bzero-ssm-agent/blob/76d133c565bb7e11683f63fbc23d39fa0840df14/core/agent.go#L89
// block until we receive a SIGINT or SIGTERM
func OsShutdownChan() chan os.Signal {
	// Below channel will handle all machine initiated shutdown/reboot requests.

	// Set up channel on which to receive signal notifications.
	// We must use a buffered channel or risk missing the signal
	// if we're not ready to receive when the signal is sent.
	c := make(chan os.Signal, 1)

	// Listening for OS signals is a blocking call.
	// Only listen to signals that require us to exit.
	// Otherwise we will continue execution and exit the program.
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	return c
}
