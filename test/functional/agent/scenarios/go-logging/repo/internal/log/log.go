// Package log is the project's logger. Everything under internal/ logs
// through it.
package log

import (
	"log/slog"
	"os"
)

var logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

// Info logs msg with key/value pairs.
func Info(msg string, args ...any) { logger.Info(msg, args...) }
