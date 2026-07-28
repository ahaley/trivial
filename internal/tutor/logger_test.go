package tutor

import (
	"io"
	"log/slog"
)

// quietLogger keeps expected-failure paths from spamming test output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}
