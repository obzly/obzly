package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	loggerInstance *slog.Logger
	once           sync.Once
)

// GetLogger returns a singleton slog.Logger that logs to the console
func GetLogger() *slog.Logger {
	once.Do(func() {
		handler := slog.NewTextHandler(output(), &slog.HandlerOptions{
			Level: slog.LevelInfo,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey {
					a.Value = slog.StringValue(time.Now().Format("2006/01/02 15:04:05"))
				}

				if a.Key == slog.MessageKey {
					return slog.Attr{
						Key:   slog.MessageKey,
						Value: slog.StringValue(a.Value.String()),
					}
				}

				if a.Key == slog.LevelKey {
					return slog.Attr{}
				}

				return a
			},
		})

		loggerInstance = slog.New(handler)

		loggerInstance.Info("Text structured logger initialized")
	})

	return loggerInstance
}

// output is stdout, except for a subcommand (`main reconcile --json`),
// whose stdout is its result: decided here because packages log while
// initializing, before main runs.
func output() io.Writer {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		return os.Stderr
	}
	return os.Stdout
}
