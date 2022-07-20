package logger

import (
	"io"

	. "github.com/onsi/ginkgo/v2"
)

func MockLogger() *Logger {
	config := &Config{
		ConsoleWriters: []io.Writer{GinkgoWriter},
	}

	if logger, err := New(config); err == nil {
		return logger
	}
	return nil
}
