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
	TimeFormat string       // default: 15:04:05
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
