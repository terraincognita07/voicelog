package db

import (
	"context"
	"log/slog"
	"time"
)

// SQLite WAL grows with writes; VACUUM defragments after deletes.
// Without periodic maintenance, the DB file can balloon far beyond its
// real data size — annoying for backups, scary the first time you run
// `du`.

// Periods chosen to be ops-light: a checkpoint per week is well within
// SQLite's normal usage envelope, VACUUM monthly costs a few seconds of
// blocked writes (acceptable for a single-user bot).
const (
	checkpointPeriod = 7 * 24 * time.Hour
	vacuumPeriod     = 30 * 24 * time.Hour
)

// MaintenanceLoop runs until ctx is cancelled, performing periodic WAL
// checkpoints and (less frequently) VACUUMs. Both operations log their
// outcome at Info; failures log Warn and are not retried until the
// next tick — transient errors shouldn't crash the bot.
//
// Running it from multiple processes is harmless in principle (PRAGMA
// wal_checkpoint is idempotent; concurrent VACUUMs serialize behind
// SQLite's write lock), but by convention voicelog starts this loop ONLY
// in the mcp container (see cmd/mcp/main.go) so the two processes don't
// needlessly contend on the write lock. Don't also start it in the bot.
func MaintenanceLoop(ctx context.Context, store *DB, logger *slog.Logger) {
	checkpointTick := time.NewTicker(checkpointPeriod)
	defer checkpointTick.Stop()
	vacuumTick := time.NewTicker(vacuumPeriod)
	defer vacuumTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-checkpointTick.C:
			runCheckpoint(ctx, store, logger)
		case <-vacuumTick.C:
			runVacuum(ctx, store, logger)
		}
	}
}

func runCheckpoint(ctx context.Context, store *DB, logger *slog.Logger) {
	// PRAGMA wal_checkpoint(TRUNCATE) → 3 ints: busy, log frames, ckpt
	// frames. Truncate truncates the WAL file to zero after a full
	// checkpoint. `busy=1` means a reader was holding back — fine,
	// we'll try again next week.
	var busy, logFrames, ckptFrames int
	if err := store.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).
		Scan(&busy, &logFrames, &ckptFrames); err != nil {
		logger.Warn("db maintenance: wal_checkpoint", "err", err)
		return
	}
	logger.Info("db maintenance: wal checkpoint",
		"busy", busy, "log_frames", logFrames, "ckpt_frames", ckptFrames)
}

func runVacuum(ctx context.Context, store *DB, logger *slog.Logger) {
	t0 := time.Now()
	if _, err := store.ExecContext(ctx, `VACUUM`); err != nil {
		logger.Warn("db maintenance: vacuum", "err", err)
		return
	}
	var pageCount, pageSize int64
	_ = store.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount)
	_ = store.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize)
	logger.Info("db maintenance: vacuum",
		"duration_ms", time.Since(t0).Milliseconds(),
		"db_bytes", pageCount*pageSize)
}
