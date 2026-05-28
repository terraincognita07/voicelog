package db_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/terraincognita07/voicelog/internal/db"
	"github.com/terraincognita07/voicelog/internal/db/migrations"
)

// Fuzz targets for the DB-side functions that take user-provided
// strings: SearchNotes (FTS5 MATCH expression) and AddVocab (free-form
// term). Goal: prove neither one can panic regardless of input bytes.

// openFuzzDB is the testing.F equivalent of openTestDB — we can't reuse
// the *testing.T helper directly because *testing.F's lifecycle is
// different (seed-corpus build happens in F, the per-iteration run gets
// a fresh *testing.T). The DB lives for the entire fuzz run.
func openFuzzDB(tb testing.TB) *db.DB {
	tb.Helper()
	ctx := context.Background()
	path := filepath.Join(tb.TempDir(), "fuzz.db")
	d, err := db.Open(ctx, path)
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(ctx, migrations.FS); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	return d
}

func FuzzSearchNotes_QueryString(f *testing.F) {
	d := openFuzzDB(f)
	ctx := context.Background()
	// Seed a handful of notes so FTS5 has something to match against.
	for _, body := range []string{
		"купить молоко завтра",
		"call mom about the move",
		"deploy whisper container",
		"meeting notes Q2 plans",
	} {
		if _, err := d.InsertNote(ctx, body, 5); err != nil {
			f.Fatalf("seed: %v", err)
		}
	}
	for _, seed := range []string{
		"молоко",
		`"молоко"`,
		"call OR deploy",
		"meet*",
		"NEAR(deploy whisper, 5)",
		"",                            // empty → must error, not crash
		" ",                           // whitespace only → must error
		"unclosed\"quote",             // malformed FTS5 syntax
		"((((((((",                    // unbalanced
		"\x00\xff\xfe",                // control bytes
		strings.Repeat("term ", 1000), // long input
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, query string) {
		// Contract: SearchNotes may return a syntax/empty-query error,
		// or zero or more matches. It must NEVER panic.
		got, err := d.SearchNotes(ctx, query, 20, false)
		if err == nil && got == nil {
			// nil slice from no-match is allowed (driver-dependent).
			return
		}
		_ = got
		_ = err
	})
}

func FuzzAddVocab_Term(f *testing.F) {
	d := openFuzzDB(f)
	ctx := context.Background()
	for _, seed := range []string{
		"Иннокентий",
		"Bob",
		"  with surrounding  ",
		"",
		" \t\n",
		"two words",
		strings.Repeat("x", 1024),
		"\x00null",
		"\xff\xfecontrol",
		"emoji 🤖 mixed",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, term string) {
		// Contract: AddVocab returns (bool, error). Errors must be
		// real DB errors (none expected here — input is parameterized),
		// not panics. The boolean reflects whether a row was inserted.
		_, err := d.AddVocab(ctx, term)
		if err != nil && !errors.Is(err, context.Canceled) {
			// Surface unexpected errors so the fuzz fails loudly on a
			// regression. We don't pre-classify what's allowed because
			// the only path that legitimately errors is a DB-level
			// failure, which would also fail every other test.
			t.Errorf("unexpected error for term %q: %v", term, err)
		}
	})
}
