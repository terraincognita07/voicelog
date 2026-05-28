package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ArchiveAndUpdateText replaces a note's raw_text + confidence metadata
// atomically: the old raw_text is appended to notes_history (with the
// optional model string), then the notes row is updated. The whole
// operation is wrapped in a transaction — if anything fails, the note
// stays at its old text rather than ending up partial.
//
// Confidence/suspect pass through the same nil-pointer convention as
// InsertNoteWithMeta: nil → store NULL (= "unknown").
//
// Returns the old raw_text so the caller can show a diff.
func (db *DB) ArchiveAndUpdateText(ctx context.Context, id int64, newText, model string, meta NoteMeta) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	// Defer rollback; commit() at the end overrides this if all went well.
	defer func() { _ = tx.Rollback() }()

	var oldText string
	if err := tx.QueryRowContext(ctx, `SELECT raw_text FROM notes WHERE id = ?`, id).Scan(&oldText); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNoteNotFound
		}
		return "", err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO notes_history (note_id, raw_text, replaced_at, model)
		VALUES (?, ?, ?, ?)`,
		id, oldText, time.Now().Unix(), model); err != nil {
		return "", err
	}

	var overall, worst any
	if meta.ConfidenceOverall != nil {
		overall = *meta.ConfidenceOverall
	}
	if meta.ConfidenceMin != nil {
		worst = *meta.ConfidenceMin
	}
	suspect := 0
	if meta.SuspectHallucination {
		suspect = 1
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE notes
		SET raw_text = ?, confidence_overall = ?, confidence_min = ?, suspect_hallucination = ?
		WHERE id = ?`,
		newText, overall, worst, suspect, id); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return oldText, nil
}
