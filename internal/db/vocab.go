package db

import (
	"context"
	"strings"
	"time"
)

// MaxVocabTerms caps the vocabulary list to keep the whisper prompt
// bounded. whisper.cpp's prompt is silently truncated past ~224 tokens; an
// upper bound here prevents accidental DoS of transcription quality by an
// unbounded user-added list.
const MaxVocabTerms = 200

// AddVocab inserts a term. Dedup is by Unicode-folded lowercase
// (strings.ToLower) — SQLite's COLLATE NOCASE is ASCII-only. Stores the
// term in its original casing so the whisper prompt keeps the user's
// preferred form (e.g. "Иннокентий", not "иннокентий"). Returns (added,
// error) where added is true if the row did not exist before.
func (db *DB) AddVocab(ctx context.Context, term string) (bool, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return false, nil
	}
	lower := strings.ToLower(term)
	res, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO vocabulary (term_lower, term, added_at) VALUES (?, ?, ?)`,
		lower, term, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RemoveVocab deletes a term, matching case-insensitively. Returns true if
// a row was deleted.
func (db *DB) RemoveVocab(ctx context.Context, term string) (bool, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return false, nil
	}
	res, err := db.ExecContext(ctx,
		`DELETE FROM vocabulary WHERE term_lower = ?`, strings.ToLower(term))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListVocab returns every term in its stored (original) casing, ordered by
// added_at ASC (oldest first).
func (db *DB) ListVocab(ctx context.Context) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT term FROM vocabulary ORDER BY added_at ASC, term ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ClearVocab drops every row. Returns the number of rows deleted.
func (db *DB) ClearVocab(ctx context.Context) (int, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM vocabulary`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// VocabPrompt returns up to MaxVocabTerms terms joined by spaces, suitable
// as a whisper prompt suffix. Returns "" when the table is empty.
func (db *DB) VocabPrompt(ctx context.Context) (string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT term FROM vocabulary ORDER BY added_at ASC, term ASC LIMIT ?`, MaxVocabTerms)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	first := true
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return "", err
		}
		if !first {
			b.WriteByte(' ')
		}
		b.WriteString(t)
		first = false
	}
	return b.String(), rows.Err()
}
