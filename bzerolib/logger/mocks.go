package logger

func MockLogger() *Logger {
	if logger, err := NewWithNoConsoleWriters(DefaultLoggerConfig(Debug.String()), "/dev/null"); err == nil {
		return logger
	}
	return nil
}
