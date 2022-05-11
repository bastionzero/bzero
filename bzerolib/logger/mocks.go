package logger

func MockLogger() *Logger {
	if logger, err := New(DefaultLoggerConfig(Debug.String()), "/dev/null", false); err == nil {
		return logger
	}
	return nil
}
