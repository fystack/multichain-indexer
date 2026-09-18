package logger

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lmittmann/tint"
)

var (
	once   sync.Once
	logger *slog.Logger
)

type Options struct {
	Level      slog.Leveler // slog.LevelInfo, slog.LevelDebug, etc.
	Mode       string       // stdout, file, or both (default: stdout)
	Format     string       // pretty or json (default: pretty)
	Writer     io.Writer    // overrides Mode; useful for tests
	TimeFormat string       // default: 2025-07-25T10:41:10+07:00
}

// Init configures the process logger. File output is appended to logs/indexer.log.
func Init(opts *Options) error {
	var initErr error
	once.Do(func() {
		writer, err := output(opts)
		if err != nil {
			initErr = err
			return
		}

		var handler slog.Handler
		if opts.Format == "json" {
			handler = slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: opts.Level, ReplaceAttr: redactAttr})
		} else {
			handler = tint.NewHandler(writer, &tint.Options{Level: opts.Level, TimeFormat: opts.TimeFormat, ReplaceAttr: redactAttr})
		}

		logger = slog.New(handler)
		slog.SetDefault(logger)
	})
	return initErr
}

// InitFromConfig is a convenience wrapper around Init for the common case of
// deriving the log level from a --debug flag and the destination/encoding
// from config.LoggingConfig's Mode/Format fields.
func InitFromConfig(mode, format string, debug bool) error {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return Init(&Options{
		Level:      level,
		Mode:       mode,
		Format:     format,
		TimeFormat: time.RFC3339,
	})
}

func output(opts *Options) (io.Writer, error) {
	if opts.Writer != nil {
		return opts.Writer, nil
	}
	mode := opts.Mode
	if mode == "" {
		mode = "stdout"
	}
	if mode == "stdout" {
		return os.Stdout, nil
	}
	if mode != "file" && mode != "both" {
		return nil, fmt.Errorf("invalid logging mode %q", mode)
	}
	if err := os.MkdirAll("logs", 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	file, err := os.OpenFile("logs/indexer.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	if mode == "file" {
		return file, nil
	}
	return io.MultiWriter(os.Stdout, file), nil
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

var (
	// Scheme matches any URI scheme (RFC 3986), not just the ones this codebase
	// happens to use today, so newly added protocols are redacted automatically.
	urlInText = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	keyValue  = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|authorization|credential)\b\s*([=:])\s*[^\s,;"']+`)
)

// redactAttr is installed in the slog handler, so it also covers child loggers
// and direct slog calls after Init.
func redactAttr(_ []string, attr slog.Attr) slog.Attr {
	if isSensitiveKey(attr.Key) {
		return slog.String(attr.Key, "[REDACTED]")
	}
	if isURLKey(attr.Key) {
		if attr.Value.Kind() == slog.KindString {
			return slog.String(attr.Key, redactURL(attr.Value.String()))
		}
		return slog.String(attr.Key, "[REDACTED_URL]")
	}

	if attr.Value.Kind() == slog.KindAny {
		if err, ok := attr.Value.Any().(error); ok {
			return slog.String(attr.Key, redactText(err.Error()))
		}
	}
	if attr.Value.Kind() == slog.KindString {
		return slog.String(attr.Key, redactText(attr.Value.String()))
	}
	return attr
}

func isSensitiveKey(key string) bool {
	key = strings.ToLower(key)
	return key == "auth" || containsAny(key,
		"password", "passwd", "secret", "token",
		"api_key", "api-key", "authorization", "credential")
}

func isURLKey(key string) bool {
	key = strings.ToLower(key)
	return key == "url" || strings.HasSuffix(key, "_url") || strings.HasSuffix(key, "-url") ||
		strings.Contains(key, "endpoint")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func redactText(text string) string {
	// Cheap substring checks skip the regexes for the common case of log text
	// that could never match, since ReplaceAllString scans the whole string.
	if strings.Contains(text, "://") {
		text = urlInText.ReplaceAllStringFunc(text, redactURL)
	}
	if strings.ContainsAny(text, "=:") {
		text = keyValue.ReplaceAllString(text, "$1$2[REDACTED]")
	}
	return text
}

// redactURL retains the origin for operational debugging while omitting every
// component that commonly carries a credential: user info, paths, and queries.
// A bare host:port is used by Sui gRPC nodes, so retain it as well.
func redactURL(raw string) string {
	trimmed := strings.TrimRight(raw, ".,;:)]}")
	suffix := strings.TrimPrefix(raw, trimmed)
	if !strings.Contains(trimmed, "://") {
		if at := strings.LastIndex(trimmed, "@"); at >= 0 {
			trimmed = trimmed[at+1:]
		}
		if query := strings.IndexByte(trimmed, '?'); query >= 0 {
			trimmed = trimmed[:query]
		}
		return trimmed + suffix
	}

	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "[REDACTED_URL]" + suffix
	}
	return u.Scheme + "://" + u.Host + suffix
}
