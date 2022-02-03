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

	// zerolog.Logger cannot == zerolog.Logger{}, so ready lets us tell if we can log or not
	ready bool
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
			return &Logger{
				ready: false,
			}, err
		}

		consoleWriter := zerolog.ConsoleWriter{Out: os.Stdout}
		multi := zerolog.MultiLevelWriter(consoleWriter, logFile)

		return &Logger{
			logger: zerolog.New(multi).With().Timestamp().Logger(),
			ready:  true,
		}, nil
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

func (l *Logger) GetPluginLogger(pluginName string) *Logger {
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
