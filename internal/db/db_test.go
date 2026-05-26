package db_test

import (
	"context"
	"path/filepath"
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
