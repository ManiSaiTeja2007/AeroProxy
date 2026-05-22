package logger

import (
	"go.uber.org/zap"
)

// Log is the global production structured logger.
var Log *zap.Logger

func init() {
	// Initialize with Nop to prevent Nil Pointer Dereference before explicit configuration.
	Log = zap.NewNop()
}

// InitLogger boots up the production Zap structured JSON logger.
func InitLogger() error {
	logger, err := zap.NewProduction()
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
