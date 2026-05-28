package db

import (
	"context"
	"fmt"
)

// HealthReport summarizes the result of a quick DB integrity check.
//
// IntegrityCheck is the full `PRAGMA integrity_check` result — "ok"
// on a healthy DB, the first corruption message otherwise. The check
// scans every page, so on a multi-GB DB it can take tens of seconds.
// When the caller asked for a quick-only health (Health(ctx, true)),
// IntegrityCheck is the literal string "skipped" and the field stays
// distinguishable from a real failure.
type HealthReport struct {
	IntegrityCheck string `json:"integrity_check"`
	QuickCheck     string `json:"quick_check"`
	NoteCount      int64  `json:"note_count"`
	DBSizeBytes    int64  `json:"db_size_bytes"`
}

// IntegrityCheckSkipped is the sentinel `HealthReport.IntegrityCheck`
// value when Health was called with quickOnly=true. Caller can match
// against it to render a different UI (e.g. "full check pending") and
// to keep "skipped" distinct from real corruption messages.
const IntegrityCheckSkipped = "skipped"

// Health runs SQLite's PRAGMA integrity_check + quick_check + note
// count + db size. If quickOnly is true, integrity_check is skipped
// and IntegrityCheck is set to IntegrityCheckSkipped — useful on
// multi-GB DBs where the full scan would blow past a 30s tool
// timeout. quick_check still runs because it is bounded (single B-tree
// walk, not the full content scan).
//
// integrity_check / quick_check return the literal string "ok" on a
// healthy DB; anything else is the first error message.
func (db *DB) Health(ctx context.Context, quickOnly bool) (HealthReport, error) {
	var rep HealthReport
	if quickOnly {
		rep.IntegrityCheck = IntegrityCheckSkipped
	} else {
		if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&rep.IntegrityCheck); err != nil {
			return rep, fmt.Errorf("integrity_check: %w", err)
		}
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
