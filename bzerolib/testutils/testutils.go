package testutils

import (
	"encoding/base64"
	"testing"

	"bastionzero.com/bctl/v1/bctl/agent/utility"
	"bastionzero.com/bctl/v1/bzerolib/logger"
)

func MockLogger() *logger.Logger {
	logger, err := logger.New(logger.DefaultLoggerConfig(logger.Debug.String()), "/dev/null", false)
	if err == nil {
		return logger
	}
	return nil
}

// TODO: remove this https://commonwealthcrypto.atlassian.net/browse/CWC-1644
func B64Encode(b []byte) []byte {
	// Adds quotes as the base64 encoded strings which receive gets over the data channel has quotes
	return []byte("\"" + base64.StdEncoding.EncodeToString(b) + "\"")
}

func GetRunAsUser(t *testing.T) string {
	username, err := utility.WhoAmI()
	if err != nil {
		t.Errorf("Could not resolve username: %v", err)
	}
	return username
}
