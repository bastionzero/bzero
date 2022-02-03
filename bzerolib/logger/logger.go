package logger

import (
	"errors"
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/rs/zerolog/pkgerrors"
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
}

func New(debugLevel DebugLevel, logFilePath string) (*Logger, error) {
	// Let's us display stack info on errors
	zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack
	zerolog.SetGlobalLevel(debugLevel)

	// If the log file doesn't exist, create it, or append to the file
	if logFilePath != "" {
		logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("error: %s", err)
			return nil, err
		}

		consoleWriter := zerolog.ConsoleWriter{Out: os.Stdout}
		multi := zerolog.MultiLevelWriter(consoleWriter, logFile)

		return &Logger{
			logger: zerolog.New(multi).With().Timestamp().Logger(),
		}, nil
	} else {
		return &Logger{
			logger: zerolog.New(os.Stdout).With().Timestamp().Logger(),
		}, nil
	}
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
		logger: l.logger.With().Str("datachannel", id).Logger(),
	}
}

func (l *Logger) GetWebsocketLogger(id string) *Logger {
	return &Logger{
		logger: l.logger.With().Str("websocket", id).Logger(),
	}
}

func (l *Logger) GetPluginLogger(pluginName string) *Logger {
	return &Logger{
		logger: l.logger.With().Str("plugin", string(pluginName)).Logger(),
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
	l.logger = l.logger.With().Str("requestId", rid).Logger()
}

func (l *Logger) AddField(key string, value string) {
	l.logger = l.logger.With().Str(key, value).Logger()
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
