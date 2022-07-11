package logger

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/pkgerrors"
	"gopkg.in/natefinch/lumberjack.v2"

	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
)

const (
	defaultMaxLogFileSize    = 100 // MB
	defaultMaxLogFileBackups = 10
	defaultMaxLogFileAge     = 30 // days
)

// This is here for translation, so that the rest of the program doesn't need to care or know
// about zerolog
type DebugLevel = zerolog.Level

const (
	Debug DebugLevel = zerolog.DebugLevel
	Info  DebugLevel = zerolog.InfoLevel
	Error DebugLevel = zerolog.ErrorLevel
	Trace DebugLevel = zerolog.TraceLevel
)

type Config struct {
	// The global log level
	// defaults to debug
	LogLevel DebugLevel

	// Path to log file location. If no path is specified then logs
	// will not be written to files
	FilePath string

	// Output writers for log output
	ConsoleWriters []io.Writer

	// maxSize the max size in MB of the logfile before it's rolled
	// defaults to 100MB
	maxSize int

	// maxBackups the max number of rolled files to keep
	// defaults to 10
	maxBackups int

	// maxAge the max age in days to keep a logfile
	// defaults to 30 days
	maxAge int
}

func (c *Config) fillDefaults() {
	if c.LogLevel == 0 {
		c.LogLevel = Debug
	}

	if c.FilePath != "" {
		c.maxSize = defaultMaxLogFileSize
		c.maxBackups = defaultMaxLogFileBackups
		c.maxAge = defaultMaxLogFileAge
	}
}

type Logger struct {
	logger zerolog.Logger
}

func New(config *Config) (*Logger, error) {
	config.fillDefaults() // populate any config defaults

	// Set global log level
	zerolog.SetGlobalLevel(config.LogLevel)

	// Let's us display stack info on errors
	zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack
	zerolog.TimeFieldFormat = time.StampMilli

	var writers []io.Writer
	if config.FilePath != "" {
		// make our directory if it doesn't exist already
		logDir := filepath.Dir(config.FilePath)
		if err := os.MkdirAll(logDir, os.ModePerm); err != nil {
			return nil, fmt.Errorf("failed to create log directory %s", logDir)
		}

		logFileWithRotation := &lumberjack.Logger{
			Filename:   config.FilePath,
			MaxSize:    config.maxSize,
			MaxBackups: config.maxBackups,
			MaxAge:     config.maxAge,
		}

		writers = append(writers, logFileWithRotation)
	}

	// Add console writers for all specified io.Writer's
	for _, dest := range config.ConsoleWriters {
		consoleWriter := zerolog.ConsoleWriter{Out: dest}
		writers = append(writers, consoleWriter)
	}
	multi := zerolog.MultiLevelWriter(writers...)

	logger := Logger{
		logger: zerolog.New(multi).With().Timestamp().Logger(),
	}

	return &logger, nil
}

func (l *Logger) AddAgentVersion(version string) {
	l.logger = l.logger.With().Str("agentVersion", version).Logger()
}

func (l *Logger) AddDaemonVersion(version string) {
	l.logger = l.logger.With().Str("daemonVersion", version).Logger()
}

func (l *Logger) GetControlChannelLogger(id string) *Logger {
	return &Logger{
		logger: l.logger.With().Str("controlchannel", id).Logger(),
	}
}

func (l *Logger) GetDatachannelLogger(id string) *Logger {
	return &Logger{
		logger: l.logger.With().
			Str("datachannel", id).
			Logger(),
	}
}

func (l *Logger) GetWebsocketLogger(id string) *Logger {
	return &Logger{
		logger: l.logger.With().
			Str("websocket", id).
			Logger(),
	}
}

func (l *Logger) GetPluginLogger(pluginName bzplugin.PluginName) *Logger {
	return &Logger{
		logger: l.logger.With().
			Str("plugin", string(pluginName)).
			Logger(),
	}
}

func (l *Logger) GetActionLogger(actionName string) *Logger {
	return &Logger{
		logger: l.logger.With().
			Str("action", actionName).
			Logger(),
	}
}

func (l *Logger) GetComponentLogger(component string) *Logger {
	return &Logger{
		logger: l.logger.With().
			Str("component", component).
			Logger(),
	}
}

func (l *Logger) AddRequestId(rid string) {
	l.logger = l.logger.With().
		Str("requestId", rid).
		Logger()
}

func (l *Logger) AddField(key string, value string) {
	l.logger = l.logger.With().
		Str(key, value).
		Logger()
}

func (l *Logger) Info(msg string) {
	l.logger.Info().
		Msg(msg)
}

func (l *Logger) Infof(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	l.Info(msg)
}

func (l *Logger) Debug(msg string) {
	l.logger.Debug().
		Msg(msg)
}

func (l *Logger) Debugf(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	l.Debug(msg)
}

func (l *Logger) Error(err error) {
	l.logger.Error().
		Stack(). // stack trace for errors woot
		Msg(err.Error())
}

func (l *Logger) Errorf(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	l.Error(errors.New(msg))
}

func (l *Logger) Trace(msg string) {
	l.logger.Trace().
		Msg(msg)
}

func (l *Logger) Tracef(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	l.Trace(msg)
}
