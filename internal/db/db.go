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

// Migrate applies every *.sql file in files (lexicographic order). All
// statements use IF NOT EXISTS, so the operation is idempotent.
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
	for _, name := range names {
		content, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if _, err := db.ExecContext(ctx, string(content)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return nil
}
