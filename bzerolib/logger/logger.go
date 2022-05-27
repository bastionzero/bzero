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

// This is here for translation, so that the rest of the program doesn't need to care or know
// about zerolog
type DebugLevel = zerolog.Level

const (
	Debug DebugLevel = zerolog.DebugLevel
	Info  DebugLevel = zerolog.InfoLevel
	Error DebugLevel = zerolog.ErrorLevel
	Trace DebugLevel = zerolog.TraceLevel
)

type Logger struct {
	logger zerolog.Logger

	// zerolog.Logger cannot == zerolog.Logger{}, so ready lets us tell if we can log or not
	ready bool
}

type LoggerConfig struct {
	// The default global log level
	LogLevel DebugLevel

	// MaxSize the max size in MB of the logfile before it's rolled
	MaxSize int

	// MaxBackups the max number of rolled files to keep
	MaxBackups int

	// MaxAge the max age in days to keep a logfile
	MaxAge int
}

func DefaultLoggerConfig(logLevel string) *LoggerConfig {
	level, err := zerolog.ParseLevel(logLevel)
	if err != nil {
		level = zerolog.DebugLevel
	}

	return &LoggerConfig{
		LogLevel:   DebugLevel(level),
		MaxSize:    100,
		MaxBackups: 10,
		MaxAge:     30,
	}
}

func NewWithStdOutConsoleWriter(config *LoggerConfig, logFilePath string) (*Logger, error) {
	return New(config, logFilePath, []io.Writer{os.Stdout})
}

func NewWithNoConsoleWriters(config *LoggerConfig, logFilePath string) (*Logger, error) {
	return New(config, logFilePath, []io.Writer{})
}

func New(config *LoggerConfig, logFilePath string, consoleWriterDestinations []io.Writer) (*Logger, error) {
	// Let's us display stack info on errors
	zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack
	zerolog.TimeFieldFormat = time.StampMilli
	zerolog.SetGlobalLevel(config.LogLevel)

	// If the log file doesn't exist, create it, or append to the file
	if logFilePath != "" {
		// make our directory if it doesn't exist already
		// TODO: do this in our install process
		logDir := filepath.Dir(logFilePath)
		if err := os.MkdirAll(logDir, os.ModePerm); err != nil {
			return nil, fmt.Errorf("failed to create log directory %s", logDir)
		}

		logFileWithRotation := &lumberjack.Logger{
			Filename:   logFilePath,
			MaxSize:    config.MaxSize, // megabytes
			MaxBackups: config.MaxBackups,
			MaxAge:     config.MaxAge, //days
			// Compress:   true
		}

		writers := []io.Writer{logFileWithRotation}

		// Add console writers for all specified io.Writer destinations
		for _, dest := range consoleWriterDestinations {
			consoleWriter := zerolog.ConsoleWriter{Out: dest}
			writers = append(writers, consoleWriter)
		}
		multi := zerolog.MultiLevelWriter(writers...)

		logger := Logger{
			logger: zerolog.New(multi).With().Timestamp().Logger(),
			ready:  true,
		}

		return &logger, nil
	} else {
		return &Logger{
			logger: zerolog.New(os.Stdout).With().Timestamp().Logger(),
			ready:  true,
		}, nil
	}
}

func (l *Logger) AddAgentVersion(version string) {
	if l.ready {
		l.logger = l.logger.With().Str("agentVersion", version).Logger()
	}
}

func (l *Logger) AddDaemonVersion(version string) {
	if l.ready {
		l.logger = l.logger.With().Str("daemonVersion", version).Logger()
	}
}

func (l *Logger) GetControlChannelLogger(id string) *Logger {
	if !l.ready {
		return l
	}
	return &Logger{
		logger: l.logger.With().Str("controlchannel", id).Logger(),
		ready:  true,
	}
}

func (l *Logger) GetDatachannelLogger(id string) *Logger {
	if !l.ready {
		return l
	}
	return &Logger{
		logger: l.logger.With().Str("datachannel", id).Logger(),
		ready:  true,
	}
}

func (l *Logger) GetWebsocketLogger(id string) *Logger {
	if !l.ready {
		return l
	}
	return &Logger{
		logger: l.logger.With().Str("websocket", id).Logger(),
		ready:  true,
	}
}

func (l *Logger) GetPluginLogger(pluginName bzplugin.PluginName) *Logger {
	if !l.ready {
		return l
	}
	return &Logger{
		logger: l.logger.With().Str("plugin", string(pluginName)).Logger(),
		ready:  true,
	}
}

func (l *Logger) GetActionLogger(actionName string) *Logger {
	if !l.ready {
		return l
	}
	return &Logger{
		logger: l.logger.With().
			Str("action", actionName).
			Logger(),
		ready: true,
	}
}

func (l *Logger) GetComponentLogger(component string) *Logger {
	if !l.ready {
		return l
	}
	return &Logger{
		logger: l.logger.With().
			Str("component", component).
			Logger(),

		ready: true,
	}
}

func (l *Logger) AddRequestId(rid string) {
	if l.ready {
		l.logger = l.logger.With().Str("requestId", rid).Logger()
	}
}

func (l *Logger) AddField(key string, value string) {
	if l.ready {
		l.logger = l.logger.With().Str(key, value).Logger()
	}
}

func (l *Logger) Info(msg string) {
	if l.ready {
		l.logger.Info().
			Msg(msg)
	}
}

func (l *Logger) Infof(format string, a ...interface{}) {
	if l.ready {
		msg := fmt.Sprintf(format, a...)
		l.Info(msg)
	}
}

func (l *Logger) Debug(msg string) {
	if l.ready {
		l.logger.Debug().
			Msg(msg)
	}
}

func (l *Logger) Debugf(format string, a ...interface{}) {
	if l.ready {
		msg := fmt.Sprintf(format, a...)
		l.Debug(msg)
	}
}

func (l *Logger) Error(err error) {
	if l.ready {
		l.logger.Error().
			Stack(). // stack trace for errors woot
			Msg(err.Error())
	}
}

func (l *Logger) Errorf(format string, a ...interface{}) {
	if l.ready {
		msg := fmt.Sprintf(format, a...)
		l.Error(errors.New(msg))
	}
}

func (l *Logger) Trace(msg string) {
	if l.ready {
		l.logger.Trace().
			Msg(msg)
	}
}

func (l *Logger) Tracef(format string, a ...interface{}) {
	if l.ready {
		msg := fmt.Sprintf(format, a...)
		l.Trace(msg)
	}
}
