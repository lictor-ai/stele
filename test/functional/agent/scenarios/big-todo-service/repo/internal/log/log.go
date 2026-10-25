// Package log is the project's structured logger. Everything under internal/
// logs through it.
package log

import (
	"log/slog"
	"os"
)

var logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

// Info logs an informational message with key-value pairs.
func Info(msg string, kv ...any) { logger.Info(msg, kv...) }

// Warn logs a warning with key-value pairs.
func Warn(msg string, kv ...any) { logger.Warn(msg, kv...) }

// Error logs an error with key-value pairs.
func Error(msg string, kv ...any) { logger.Error(msg, kv...) }
