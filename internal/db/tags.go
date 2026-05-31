package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// MaxTagLen caps a single tag's length. Tags are short category labels
// (#идея, #todo), not prose — 64 runes mirrors the vocabulary cap.
const MaxTagLen = 64

// normalizeTag canonicalizes a tag: trim, drop a leading '#', lowercase
// (Unicode-aware via strings.ToLower so "Философия" == "философия"). Returns
// "" for tags that are empty or longer than MaxTagLen after trimming — the
// caller skips those.
func normalizeTag(tag string) string {
	t := strings.TrimSpace(tag)
	t = strings.TrimPrefix(t, "#")
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" || len([]rune(t)) > MaxTagLen {
		return ""
	}
	return t
}

// TagCount is one row of ListTags: a tag and how many notes carry it.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// AddTags attaches the given tags to a note, idempotent per (note_id, tag)
// via INSERT OR IGNORE. Tags are normalized; empty / over-length ones are
// skipped. Returns the number of (note, tag) pairs actually inserted, or
// ErrNoteNotFound if the note id is unknown.
func (db *DB) AddTags(ctx context.Context, noteID int64, tags []string) (int, error) {
	// Confirm the note exists so a typo'd id is a clear error rather than a
	// silent FK rejection on the first insert.
	var dummy int64
	err := db.QueryRowContext(ctx, `SELECT id FROM notes WHERE id = ?`, noteID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoteNotFound
	}
	if err != nil {
		return 0, err
	}
	added := 0
	for _, raw := range tags {
		t := normalizeTag(raw)
		if t == "" {
			continue
		}
		res, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO note_tags (note_id, tag) VALUES (?, ?)`, noteID, t)
		if err != nil {
			return added, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	return added, nil
}

// RemoveTag detaches one tag from a note. Returns true if a row was removed,
// false if the (note, tag) pair wasn't present.
func (db *DB) RemoveTag(ctx context.Context, noteID int64, tag string) (bool, error) {
	t := normalizeTag(tag)
	if t == "" {
		return false, nil
	}
	res, err := db.ExecContext(ctx,
		`DELETE FROM note_tags WHERE note_id = ? AND tag = ?`, noteID, t)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TagsForNote returns a single note's tags, alphabetical.
func (db *DB) TagsForNote(ctx context.Context, noteID int64) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT tag FROM note_tags WHERE note_id = ? ORDER BY tag`, noteID)
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

// TagsForNotes batch-loads tags for many notes in one query (avoids N+1).
// Returns a map keyed by note_id; notes with no tags are simply absent.
// Used by the MCP read tools and the bot list views.
func (db *DB) TagsForNotes(ctx context.Context, ids []int64) (map[int64][]string, error) {
	out := make(map[int64][]string)
	if len(ids) == 0 {
		return out, nil
	}
	// placeholders built from len(ids); values bound via args — no user
	// input is interpolated into the SQL.
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		`SELECT note_id, tag FROM note_tags WHERE note_id IN (%s) ORDER BY tag`, placeholders), args...) // nosemgrep
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t string
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = append(out[id], t)
	}
	return out, rows.Err()
}

// ListTags returns every distinct tag with the number of notes carrying it,
// most-used first then alphabetical — the vocabulary of categories in use.
func (db *DB) ListTags(ctx context.Context) ([]TagCount, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT tag, COUNT(*) AS n
		FROM note_tags
		GROUP BY tag
		ORDER BY n DESC, tag ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var tc TagCount
		if err := rows.Scan(&tc.Tag, &tc.Count); err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

// NotesByTag returns notes carrying the given tag, newest first. The tag is
// normalized so "#Идея" and "идея" are equivalent. limit ≤ 0 or
// > MaxNotesInRange clamps to MaxNotesInRange.
func (db *DB) NotesByTag(ctx context.Context, tag string, limit int) ([]Note, error) {
	t := normalizeTag(tag)
	if t == "" {
		return nil, errors.New("empty tag")
	}
	if limit <= 0 || limit > MaxNotesInRange {
		limit = MaxNotesInRange
	}
	return queryNotes(ctx, db, `
		SELECT n.id, n.created_at, n.raw_text, n.duration_sec, n.audio_path, n.status,
		       n.confidence_overall, n.confidence_min, n.suspect_hallucination
		FROM notes n
		JOIN note_tags t ON t.note_id = n.id
		WHERE t.tag = ?
		ORDER BY n.created_at DESC, n.id DESC
		LIMIT ?`, t, limit)
}
