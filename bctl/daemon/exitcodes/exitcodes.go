package exitcodes

import (
	"errors"
	"os"

	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

// Daemon Exit Codes
const (
	SUCCESS               = 0
	UNSPECIFIED_ERROR     = 1
	BZCERT_ID_TOKEN_ERROR = 2
)

// Checks if the error is a specially handled error where we should exit the
// daemon process with a specific exit code
func HandleDaemonError(err error, logger *logger.Logger) {
	// https://go.dev/blog/go1.13-errors target
	// Check if the error is either a bzcert.InitialIdTokenError (IdP key
	// rotation) or bzcert.CurrentIdTokenError (id token needs to be
	// refreshed) token error and prompt user to re-login
	var initialIdTokenError *bzcert.InitialIdTokenError
	var currentIdTokenError *bzcert.InitialIdTokenError
	if errors.As(err, &initialIdTokenError) || errors.As(err, &currentIdTokenError) {
		logger.Errorf("Error constructing BastionZero certificate: %s", err)
		logger.Errorf("IdP tokens are invalid/expired. Please try to re-login with the zli.")
		os.Exit(BZCERT_ID_TOKEN_ERROR)
	}
}
