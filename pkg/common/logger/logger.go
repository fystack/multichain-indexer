package logger

import (
	"log/slog"
	"os"
	"sync"

	"github.com/lmittmann/tint"
)

var (
	once   sync.Once
	logger *slog.Logger
)

type Options struct {
	Level      slog.Leveler // slog.LevelInfo, slog.LevelDebug, etc.
	Writer     *os.File     // default: os.Stdout
	TimeFormat string       // default: 2025-07-25T10:41:10+07:00
}

func Init(opts *Options) {
	once.Do(func() {
		writer := opts.Writer
		if writer == nil {
			writer = os.Stdout
		}

		handler := tint.NewHandler(writer, &tint.Options{
			Level:      opts.Level,
			TimeFormat: opts.TimeFormat,
		})

		logger = slog.New(handler)
		slog.SetDefault(logger)
	})
}

func L() *slog.Logger {
	return logger
}

// Info logs at info level.
func Info(msg string, args ...any) {
	if logger != nil {
		logger.Info(msg, args...)
	} else {
		slog.Info(msg, args...)
	}
}

// Debug logs at debug level.
func Debug(msg string, args ...any) {
	if logger != nil {
		logger.Debug(msg, args...)
	} else {
		slog.Debug(msg, args...)
	}
}

// Warn logs at warn level.
func Warn(msg string, args ...any) {
	if logger != nil {
		logger.Warn(msg, args...)
	} else {
		slog.Warn(msg, args...)
	}
}

// Error logs at error level.
func Error(msg string, args ...any) {
	if logger != nil {
		logger.Error(msg, args...)
	} else {
		slog.Error(msg, args...)
	}
}

// Fatal logs an error then exits.
func Fatal(msg string, args ...any) {
	Error(msg, args...)
	os.Exit(1)
}

func With(args ...any) *slog.Logger {
	return logger.With(args...)
}
