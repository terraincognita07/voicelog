package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

// Open returns a *DB ready for use. dsn is a file path or ":memory:".
// WAL + busy_timeout are applied via DSN pragmas.
func Open(ctx context.Context, dsn string) (*DB, error) {
	full := dsn + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	d, err := sql.Open("sqlite", full)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := pingWithBusyRetry(ctx, d); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return &DB{DB: d}, nil
}

// pingWithBusyRetry calls PingContext with exponential backoff on
// SQLITE_BUSY. The race we absorb: on a fresh DB file, the first
// query applies the _pragma=journal_mode(WAL) DSN parameter, which
// is itself a write. Two concurrent Opens — the typical bot + mcp
// docker-compose-up against a brand-new ./data — can both reach for
// that write before busy_timeout (also still being applied) takes
// effect; the loser gets SQLITE_BUSY (5). Once journal_mode is WAL,
// every subsequent connection in either pool is fine.
//
// Budget ≈ 3.15s (50+100+200+400+800+1600 ms). Non-busy errors return
// immediately. Honors ctx cancellation between attempts.
func pingWithBusyRetry(ctx context.Context, d *sql.DB) error {
	backoff := 50 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		err := d.PingContext(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isSQLiteBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return lastErr
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	// modernc/sqlite encodes the busy result as "SQLITE_BUSY" in the
	// error text. Substring match keeps this file driver-agnostic; no
	// need to import the modernc-specific error type.
	return strings.Contains(err.Error(), "SQLITE_BUSY")
}

// Migrate applies every *.sql file in files (lexicographic order). A
// schema_migrations table tracks applied names so non-idempotent
// statements (ALTER ADD COLUMN, for one) run exactly once.
//
// The whole body runs inside a single BEGIN IMMEDIATE transaction on a
// dedicated connection. Two effects:
//
//  1. Concurrent runners on the same DB file (e.g. bot + mcp starting
//     in parallel against a fresh ./data) serialize on SQLite's RESERVED
//     lock instead of racing through autocommitted DDL — the loser sees
//     the winner's schema_migrations rows and skips the work. Closes F1.
//  2. If any statement inside a migration fails (disk full, syntax
//     error, SIGKILL mid-apply), the whole file rolls back together
//     with its schema_migrations row — no half-applied migrations that
//     would fail re-application with "duplicate column name". Closes F2.
//
// Backwards compatibility: a DB that was migrated before this tracker
// existed will re-apply its earlier migrations on first run. Those
// migrations all use IF NOT EXISTS / CREATE OR REPLACE patterns, so
// re-application is a no-op. They get recorded after that first run
// and subsequently skipped.
func (db *DB) Migrate(ctx context.Context, files fs.FS) error {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migrate conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin migrate tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Best-effort rollback. Use a fresh background context so we
			// still release the lock even when the caller's ctx is gone.
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedMigrationsConn(ctx, conn)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		content, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if _, err := conn.ExecContext(ctx, string(content)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT OR REPLACE INTO schema_migrations(name, applied_at) VALUES (?, strftime('%s','now'))`,
			name); err != nil {
			return fmt.Errorf("record %s: %w", name, err)
		}
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit migrate tx: %w", err)
	}
	committed = true
	return nil
}

func appliedMigrationsConn(ctx context.Context, conn *sql.Conn) (map[string]bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}
