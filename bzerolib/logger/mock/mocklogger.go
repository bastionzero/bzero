package mock

import "bastionzero.com/bctl/v1/bzerolib/logger"

func MockLogger() *logger.Logger {
	if logger, err := logger.New(logger.DefaultLoggerConfig(logger.Debug.String()), "/dev/null", false); err == nil {
		return logger
	}
	return nil
}
