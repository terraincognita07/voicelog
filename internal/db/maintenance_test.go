package db_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/terraincognita07/voicelog/internal/db"
)

// TestMaintenanceLoop_ExitsOnContextCancel asserts the maintenance
// goroutine shuts down promptly when its parent ctx is cancelled.
// checkpointPeriod is 7 days and vacuumPeriod is 30 days; neither
// ticker fires during the test, so cancel must unblock the select
// on ctx.Done() directly.
func TestMaintenanceLoop_ExitsOnContextCancel(t *testing.T) {
	store := openTestDB(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		db.MaintenanceLoop(ctx, store, logger)
		close(done)
	}()

	// Let the goroutine enter the select before cancelling.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// shut down cleanly
	case <-time.After(2 * time.Second):
		t.Fatalf("MaintenanceLoop did not exit within 2s of ctx cancel")
	}
}
