package db_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"voicelog/internal/db"
	"voicelog/migrations"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	if err := d.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("second migrate run: %v", err)
	}
}

// TestMigrateConcurrent guards F1 (concurrent migration race) AND the
// adjacent Open-time WAL-pragma race resolved in the same session.
// Simulates bot + mcp + N additional processes hitting the same fresh
// DB file at once. Each goroutine does its own Open + Migrate, no
// warm-up — Open's pingWithBusyRetry has to absorb the journal_mode
// race, and Migrate's BEGIN IMMEDIATE has to absorb the migration
// race.
//
// Note: this is one process with several *sql.DB pools — not literally
// separate processes, but the SQLite locking surface is the same (a
// *sql.Conn from each pool is a distinct connection to the file).
func TestMigrateConcurrent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "concurrent.db")

	const N = 5
	var wg sync.WaitGroup
	errCh := make(chan error, N)
	start := make(chan struct{})

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := db.Open(ctx, dbPath)
			if err != nil {
				errCh <- err
				return
			}
			defer d.Close()
			<-start // line them up so they actually contend
			if err := d.Migrate(ctx, migrations.FS); err != nil {
				errCh <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent migrate: %v", err)
	}

	// Final-state check: schema_migrations holds exactly one row per
	// migration file (no duplicates from a race that slipped through),
	// and the notes table exists.
	d, err := db.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d.Close()

	wantMigrations, err := countMigrationFiles()
	if err != nil {
		t.Fatalf("count migration files: %v", err)
	}
	var gotMigrations int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&gotMigrations); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if gotMigrations != wantMigrations {
		t.Errorf("schema_migrations row count: got %d, want %d", gotMigrations, wantMigrations)
	}

	var notesTable string
	if err := d.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='notes'`).Scan(&notesTable); err != nil {
		t.Errorf("notes table not present after concurrent migrate: %v", err)
	}
}

func countMigrationFiles() (int, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" {
			n++
		}
	}
	return n, nil
}

func TestInsertAndFTSSearch(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	now := time.Now().Unix()
	res, err := d.ExecContext(ctx,
		`INSERT INTO notes (created_at, raw_text, duration_sec) VALUES (?, ?, ?)`,
		now, "Купить молоко и хлеб завтра утром", 4)
	if err != nil {
		t.Fatalf("insert note: %v", err)
	}
	wantID, _ := res.LastInsertId()

	var gotID int64
	var gotText string
	err = d.QueryRowContext(ctx, `
		SELECT n.id, n.raw_text
		FROM notes_fts
		JOIN notes n ON n.id = notes_fts.rowid
		WHERE notes_fts MATCH ?
		ORDER BY bm25(notes_fts)
		LIMIT 1`, "молоко").Scan(&gotID, &gotText)
	if err != nil {
		t.Fatalf("fts search: %v", err)
	}
	if gotID != wantID {
		t.Fatalf("fts returned id %d, want %d", gotID, wantID)
	}
	if gotText == "" {
		t.Fatalf("empty raw_text from join")
	}
}

func TestDeleteTriggerRemovesFromFTS(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	res, err := d.ExecContext(ctx,
		`INSERT INTO notes (created_at, raw_text) VALUES (?, ?)`,
		time.Now().Unix(), "идея для проекта voicelog")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id, _ := res.LastInsertId()

	if _, err := d.ExecContext(ctx, `DELETE FROM notes WHERE id = ?`, id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM notes_fts WHERE notes_fts MATCH ?`, "voicelog").Scan(&n); err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 FTS rows after delete, got %d", n)
	}
}

func TestUpdateTriggerRefreshesFTS(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	res, err := d.ExecContext(ctx,
		`INSERT INTO notes (created_at, raw_text) VALUES (?, ?)`,
		time.Now().Unix(), "первая редакция текста")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id, _ := res.LastInsertId()

	if _, err := d.ExecContext(ctx,
		`UPDATE notes SET raw_text = ? WHERE id = ?`,
		"вторая редакция содержит ключевое слово артефакт", id); err != nil {
		t.Fatalf("update: %v", err)
	}

	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM notes_fts WHERE notes_fts MATCH ?`, "первая").Scan(&n); err != nil {
		t.Fatalf("count old term: %v", err)
	}
	if n != 0 {
		t.Fatalf("old term still in FTS index after update, got %d rows", n)
	}

	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM notes_fts WHERE notes_fts MATCH ?`, "артефакт").Scan(&n); err != nil {
		t.Fatalf("count new term: %v", err)
	}
	if n != 1 {
		t.Fatalf("new term not found in FTS index, got %d rows", n)
	}
}

func TestStatusCheckConstraint(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	_, err := d.ExecContext(ctx,
		`INSERT INTO notes (created_at, raw_text, status) VALUES (?, ?, ?)`,
		time.Now().Unix(), "x", "bogus")
	if err == nil {
		t.Fatalf("expected CHECK constraint to reject bogus status")
	}
}
