package db

import (
	"context"
	"fmt"
)

// HealthReport summarizes the result of a quick DB integrity check.
type HealthReport struct {
	IntegrityCheck string `json:"integrity_check"`
	QuickCheck     string `json:"quick_check"`
	NoteCount      int64  `json:"note_count"`
	DBSizeBytes    int64  `json:"db_size_bytes"`
}

// Health runs SQLite's PRAGMA integrity_check and quick_check plus a
// note count and db size. integrity_check/quick_check return the
// literal string "ok" on a healthy DB; anything else is the first
// error message.
func (db *DB) Health(ctx context.Context) (HealthReport, error) {
	var rep HealthReport
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&rep.IntegrityCheck); err != nil {
		return rep, fmt.Errorf("integrity_check: %w", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&rep.QuickCheck); err != nil {
		return rep, fmt.Errorf("quick_check: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM notes`).Scan(&rep.NoteCount); err != nil {
		return rep, fmt.Errorf("note_count: %w", err)
	}
	var pageCount, pageSize int64
	if err := db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return rep, fmt.Errorf("page_count: %w", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return rep, fmt.Errorf("page_size: %w", err)
	}
	rep.DBSizeBytes = pageCount * pageSize
	return rep, nil
}
