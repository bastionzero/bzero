package logger

import (
	"io"

	. "github.com/onsi/ginkgo/v2"
)

func MockLogger() *Logger {
	if logger, err := createLogger(DefaultLoggerConfig(Debug.String()), "/dev/null", []io.Writer{GinkgoWriter}); err == nil {
		return logger
	}
	return nil
}
