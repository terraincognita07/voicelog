package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"

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
	if err := d.PingContext(ctx); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return &DB{DB: d}, nil
}

// Migrate applies every *.sql file in files (lexicographic order). A
// schema_migrations table tracks applied names so non-idempotent
// statements (ALTER ADD COLUMN, for one) run exactly once.
//
// Backwards compatibility: a DB that was migrated before this tracker
// existed will re-apply its earlier migrations on first run. Those
// migrations all use IF NOT EXISTS / CREATE OR REPLACE patterns, so
// re-application is a no-op. They get recorded after that first run
// and subsequently skipped.
func (db *DB) Migrate(ctx context.Context, files fs.FS) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	applied, err := db.appliedMigrations(ctx)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}

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
	for _, name := range names {
		if applied[name] {
			continue
		}
		content, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if _, err := db.ExecContext(ctx, string(content)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT OR REPLACE INTO schema_migrations(name, applied_at) VALUES (?, strftime('%s','now'))`,
			name); err != nil {
			return fmt.Errorf("record %s: %w", name, err)
		}
	}
	return nil
}

func (db *DB) appliedMigrations(ctx context.Context) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM schema_migrations`)
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
