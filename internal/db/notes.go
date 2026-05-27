package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusAnalyzed  Status = "analyzed"
	StatusDiscarded Status = "discarded"
)

type Note struct {
	ID                   int64
	CreatedAt            time.Time
	RawText              string
	DurationSec          sql.NullInt64
	AudioPath            sql.NullString
	Status               Status
	ConfidenceOverall    sql.NullFloat64
	ConfidenceMin        sql.NullFloat64
	SuspectHallucination bool
}

// NoteMeta carries optional transcription-quality signals computed from
// whisper's verbose_json response. All three are pointers so the caller
// can explicitly omit them (= store NULL) for the "old whisper" or
// "no segments" case — distinct from a real 0.0 confidence.
type NoteMeta struct {
	ConfidenceOverall    *float64
	ConfidenceMin        *float64
	SuspectHallucination bool
}

var ErrNoteNotFound = errors.New("note not found")

// InsertNote inserts a note without any quality metadata — older
// callsites and tests that don't have segment info should use this
// path. The confidence_* columns stay NULL and suspect_hallucination is 0.
func (db *DB) InsertNote(ctx context.Context, rawText string, durationSec int) (int64, error) {
	return db.InsertNoteWithMeta(ctx, rawText, durationSec, NoteMeta{})
}

// InsertNoteWithMeta inserts a note alongside its quality signals.
// Nil pointer fields are stored as NULL — distinct from a real 0.0.
func (db *DB) InsertNoteWithMeta(ctx context.Context, rawText string, durationSec int, meta NoteMeta) (int64, error) {
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
	res, err := db.ExecContext(ctx, `
		INSERT INTO notes (created_at, raw_text, duration_sec,
		                   confidence_overall, confidence_min, suspect_hallucination)
		VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), rawText, durationSec, overall, worst, suspect)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) CountPending(ctx context.Context) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM notes WHERE status = 'pending'`).Scan(&n)
	return n, err
}

func (db *DB) ListPending(ctx context.Context, limit int) ([]Note, error) {
	return queryNotes(ctx, db, `
		SELECT id, created_at, raw_text, duration_sec, audio_path, status,
		       confidence_overall, confidence_min, suspect_hallucination
		FROM notes
		WHERE status = 'pending'
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, limit)
}

func (db *DB) ListRecent(ctx context.Context, limit int) ([]Note, error) {
	return queryNotes(ctx, db, `
		SELECT id, created_at, raw_text, duration_sec, audio_path, status,
		       confidence_overall, confidence_min, suspect_hallucination
		FROM notes
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, limit)
}

// ListRecentByStatus is ListRecent narrowed to a single status. status=""
// is equivalent to ListRecent (no filter).
func (db *DB) ListRecentByStatus(ctx context.Context, status string, limit int) ([]Note, error) {
	if status == "" {
		return db.ListRecent(ctx, limit)
	}
	return queryNotes(ctx, db, `
		SELECT id, created_at, raw_text, duration_sec, audio_path, status,
		       confidence_overall, confidence_min, suspect_hallucination
		FROM notes
		WHERE status = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, status, limit)
}

// CountByStatus returns how many notes hold the given status. status=""
// counts all notes regardless of status.
func (db *DB) CountByStatus(ctx context.Context, status string) (int, error) {
	var n int
	if status == "" {
		err := db.QueryRowContext(ctx, `SELECT count(*) FROM notes`).Scan(&n)
		return n, err
	}
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM notes WHERE status = ?`, status).Scan(&n)
	return n, err
}

func (db *DB) MarkDiscarded(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx,
		`UPDATE notes SET status = 'discarded' WHERE id = ? AND status != 'discarded'`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNoteNotFound
	}
	return nil
}

type NoteWithRank struct {
	Note
	Rank    float64
	Snippet string
}

// MaxNotesInRange caps GetNotesInRange to prevent unbounded result sets
// (memory blow-up if an MCP client supplies an extremely wide window).
const MaxNotesInRange = 500

// GetNotesInRange returns notes whose created_at falls in [from, to). If
// status is non-empty, results are filtered by status. If status is empty
// AND includeDiscarded is false, discarded notes are excluded — matches
// the user's "forget this" intent. limit ≤ 0 or > MaxNotesInRange is
// clamped to MaxNotesInRange.
func (db *DB) GetNotesInRange(ctx context.Context, from, to time.Time, status string, limit int, includeDiscarded bool) ([]Note, error) {
	if limit <= 0 || limit > MaxNotesInRange {
		limit = MaxNotesInRange
	}
	if status != "" {
		return queryNotes(ctx, db, `
			SELECT id, created_at, raw_text, duration_sec, audio_path, status,
			       confidence_overall, confidence_min, suspect_hallucination
			FROM notes
			WHERE created_at >= ? AND created_at < ? AND status = ?
			ORDER BY created_at DESC, id DESC
			LIMIT ?`, from.Unix(), to.Unix(), status, limit)
	}
	if includeDiscarded {
		return queryNotes(ctx, db, `
			SELECT id, created_at, raw_text, duration_sec, audio_path, status,
			       confidence_overall, confidence_min, suspect_hallucination
			FROM notes
			WHERE created_at >= ? AND created_at < ?
			ORDER BY created_at DESC, id DESC
			LIMIT ?`, from.Unix(), to.Unix(), limit)
	}
	return queryNotes(ctx, db, `
		SELECT id, created_at, raw_text, duration_sec, audio_path, status,
		       confidence_overall, confidence_min, suspect_hallucination
		FROM notes
		WHERE created_at >= ? AND created_at < ? AND status != 'discarded'
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, from.Unix(), to.Unix(), limit)
}

// SearchNotes runs an FTS5 MATCH and returns rows ordered by bm25 rank
// (lower rank = better match). query is passed through to FTS5 as-is, so it
// supports the full FTS5 query syntax (phrases in quotes, OR, NEAR, *).
// When includeDiscarded is false (the default callers want), discarded notes
// are filtered out — they represent the user's explicit "forget this" signal.
func (db *DB) SearchNotes(ctx context.Context, query string, limit int, includeDiscarded bool) ([]NoteWithRank, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("empty query")
	}
	statusFilter := ""
	if !includeDiscarded {
		statusFilter = ` AND n.status != 'discarded'`
	}
	rows, err := db.QueryContext(ctx, `
		SELECT n.id, n.created_at, n.raw_text, n.duration_sec, n.audio_path, n.status,
		       n.confidence_overall, n.confidence_min, n.suspect_hallucination,
		       bm25(notes_fts) AS rank,
		       snippet(notes_fts, 0, '<<', '>>', '...', 30) AS snip
		FROM notes_fts
		JOIN notes n ON n.id = notes_fts.rowid
		WHERE notes_fts MATCH ?`+statusFilter+`
		ORDER BY rank
		LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NoteWithRank
	for rows.Next() {
		var nr NoteWithRank
		var ts int64
		var status string
		var suspect int
		if err := rows.Scan(&nr.ID, &ts, &nr.RawText, &nr.DurationSec, &nr.AudioPath, &status,
			&nr.ConfidenceOverall, &nr.ConfidenceMin, &suspect, &nr.Rank, &nr.Snippet); err != nil {
			return nil, err
		}
		nr.CreatedAt = time.Unix(ts, 0)
		nr.Status = Status(status)
		nr.SuspectHallucination = suspect != 0
		out = append(out, nr)
	}
	return out, rows.Err()
}

// DiscardAllPending flips every pending note to discarded in one statement.
// Returns the number of rows actually changed (0 if no pending exists).
// Idempotent.
func (db *DB) DiscardAllPending(ctx context.Context) (int, error) {
	res, err := db.ExecContext(ctx,
		`UPDATE notes SET status = 'discarded' WHERE status = 'pending'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetNote fetches a single note by ID. Returns ErrNoteNotFound if absent.
func (db *DB) GetNote(ctx context.Context, id int64) (Note, error) {
	var n Note
	var ts int64
	var status string
	var suspect int
	err := db.QueryRowContext(ctx, `
		SELECT id, created_at, raw_text, duration_sec, audio_path, status,
		       confidence_overall, confidence_min, suspect_hallucination
		FROM notes
		WHERE id = ?`, id).Scan(&n.ID, &ts, &n.RawText, &n.DurationSec, &n.AudioPath, &status,
		&n.ConfidenceOverall, &n.ConfidenceMin, &suspect)
	if errors.Is(err, sql.ErrNoRows) {
		return Note{}, ErrNoteNotFound
	}
	if err != nil {
		return Note{}, err
	}
	n.CreatedAt = time.Unix(ts, 0)
	n.Status = Status(status)
	n.SuspectHallucination = suspect != 0
	return n, nil
}

// DiscardNotes marks multiple notes as discarded. Already-discarded rows are
// not double-counted. Returns the number of rows actually flipped.
func (db *DB) DiscardNotes(ctx context.Context, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	q := fmt.Sprintf(`UPDATE notes SET status = 'discarded' WHERE status != 'discarded' AND id IN (%s)`, placeholders)
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	res, err := db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RestoreNote flips a discarded note back to pending. Returns (true, nil) if
// the note was discarded and got restored; (false, nil) if it exists but was
// not in discarded state; ErrNoteNotFound if the id is unknown.
func (db *DB) RestoreNote(ctx context.Context, id int64) (bool, error) {
	res, err := db.ExecContext(ctx,
		`UPDATE notes SET status = 'pending' WHERE id = ? AND status = 'discarded'`, id)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, nil
	}
	// Distinguish "exists but not discarded" from "doesn't exist at all".
	var dummy int64
	err = db.QueryRowContext(ctx, `SELECT id FROM notes WHERE id = ?`, id).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNoteNotFound
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

// SetAudioPath stores the on-disk path of the retained audio file for
// the given note. No-op-safe: setting twice overwrites; an empty path
// effectively clears the field.
func (db *DB) SetAudioPath(ctx context.Context, id int64, path string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE notes SET audio_path = ? WHERE id = ?`, path, id)
	return err
}

// ClearAudioPath nulls audio_path. Called by the janitor after deleting
// the on-disk file so subsequent queries don't reference a missing path.
func (db *DB) ClearAudioPath(ctx context.Context, id int64) error {
	_, err := db.ExecContext(ctx,
		`UPDATE notes SET audio_path = NULL WHERE id = ?`, id)
	return err
}

// AudioRef pairs a note id with its retained audio file path.
type AudioRef struct {
	ID   int64
	Path string
}

// AudiosOlderThan returns every (id, path) pair where created_at < cutoff
// AND audio_path IS NOT NULL — the janitor's worklist for cleanup.
func (db *DB) AudiosOlderThan(ctx context.Context, cutoff time.Time) ([]AudioRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, audio_path FROM notes
		WHERE audio_path IS NOT NULL AND created_at < ?
		ORDER BY created_at ASC`, cutoff.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AudioRef
	for rows.Next() {
		var r AudioRef
		if err := rows.Scan(&r.ID, &r.Path); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

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

// MarkAnalyzed flips status from anything-but-discarded to 'analyzed' for the
// given ids. Returns the number of rows actually updated.
func (db *DB) MarkAnalyzed(ctx context.Context, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	q := fmt.Sprintf(`UPDATE notes SET status = 'analyzed' WHERE status != 'discarded' AND id IN (%s)`, placeholders)
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	res, err := db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func queryNotes(ctx context.Context, db *DB, query string, args ...any) ([]Note, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notes []Note
	for rows.Next() {
		var n Note
		var ts int64
		var status string
		var suspect int
		if err := rows.Scan(&n.ID, &ts, &n.RawText, &n.DurationSec, &n.AudioPath, &status,
			&n.ConfidenceOverall, &n.ConfidenceMin, &suspect); err != nil {
			return nil, err
		}
		n.CreatedAt = time.Unix(ts, 0)
		n.Status = Status(status)
		n.SuspectHallucination = suspect != 0
		notes = append(notes, n)
	}
	return notes, rows.Err()
}
