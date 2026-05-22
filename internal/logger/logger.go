package logger

import (
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Log is the global production structured logger.
var Log *zap.Logger

func init() {
	// Initialize with Nop to prevent Nil Pointer Dereference before explicit configuration.
	Log = zap.NewNop()
}

// InitLogger boots up the production Zap structured JSON logger with a specified level.
func InitLogger(levelStr string) error {
	var level zapcore.Level
	switch strings.ToLower(levelStr) {
	case "debug":
		level = zap.DebugLevel
	case "info":
		level = zap.InfoLevel
	case "error":
		level = zap.ErrorLevel
	case "warn", "warning":
		level = zap.WarnLevel
	default:
		level = zap.InfoLevel
	}

	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(level)

	logger, err := cfg.Build()
	if err != nil {
		return err
	}
	Log = logger
	return nil
}

// InitNop configures the global logger to silence all output, optimal for unit testing.
func InitNop() {
	Log = zap.NewNop()
}
