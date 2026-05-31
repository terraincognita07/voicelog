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
	StatusPending  Status = "pending"
	StatusAnalyzed Status = "analyzed"
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

// NoteMeta carries optional signals computed at insert time. Confidence
// fields are pointers so the caller can explicitly omit them (= store
// NULL) for the "old whisper" or "no segments" case — distinct from a
// real 0.0 confidence. AudioHash is "" when the caller doesn't want to
// store one (e.g. tests, retranscribe).
//
// DedupWindowSec, when > 0 AND AudioHash != "", turns InsertNoteWithMeta
// into a race-safe upsert: the INSERT is conditional on no row with the
// same audio_hash existing within the last DedupWindowSec seconds. Two
// concurrent inserts serialize through SQLite's WAL write lock, so the
// second sees the first's row and skips. The caller gets the surviving
// row's id back alongside ErrDuplicateAudio.
type NoteMeta struct {
	ConfidenceOverall    *float64
	ConfidenceMin        *float64
	SuspectHallucination bool
	AudioHash            string
	DedupWindowSec       int64
}

var (
	ErrNoteNotFound = errors.New("note not found")

	// ErrDuplicateAudio is returned by InsertNoteWithMeta when the
	// conditional INSERT ... WHERE NOT EXISTS finds an existing note
	// with the same audio_hash inside the dedup window (RowsAffected=0).
	// There is no UNIQUE constraint on audio_hash — the time-bounded
	// subquery is the dedup mechanism (see InsertNoteWithMeta). Callers
	// that handed in a non-empty AudioHash get back the existing note's
	// id alongside this error so they can render a duplicate-detected
	// reply without an extra round-trip.
	ErrDuplicateAudio = errors.New("duplicate audio_hash")
)

// MaxNotesInRange caps GetNotesInRange to prevent unbounded result sets
// (memory blow-up if an MCP client supplies an extremely wide window).
const MaxNotesInRange = 500

// maxBatchIDs bounds how many ids go into a single IN-clause. SQLite's
// SQLITE_MAX_VARIABLE_NUMBER is 32766 on current builds (and was 999 on
// pre-3.32 ones); chunking comfortably below the lower bound lets the batch
// mutators (MarkAnalyzed, DeleteNotes) accept an arbitrarily long caller-
// supplied id list without tripping "too many SQL variables". Chunks run in
// autocommit, matching the pre-chunking (non-transactional) behavior — a
// freak mid-batch failure can leave earlier chunks applied, exactly as a
// single oversized statement could partially fail. Not a concern for the id
// counts a personal journal realistically produces.
const maxBatchIDs = 500

// chunkIDs splits ids into consecutive batches of at most size. Returns nil
// for empty input; size < 1 is treated as 1.
func chunkIDs(ids []int64, size int) [][]int64 {
	if size < 1 {
		size = 1
	}
	var out [][]int64
	for i := 0; i < len(ids); i += size {
		end := i + size
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[i:end])
	}
	return out
}

// idPlaceholders builds the "?,?,…,?" fragment and the matching []any of bind
// values for an IN-clause over ids. Callers splice the fragment into the SQL
// and pass the args through Exec/Query, so no id ever reaches the query
// string. ids must be non-empty.
func idPlaceholders(ids []int64) (string, []any) {
	marks := strings.Repeat("?,", len(ids))
	marks = marks[:len(marks)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return marks, args
}

// InsertNote inserts a note without any quality metadata — older
// callsites and tests that don't have segment info should use this
// path. The confidence_* columns stay NULL and suspect_hallucination is 0.
func (db *DB) InsertNote(ctx context.Context, rawText string, durationSec int) (int64, error) {
	return db.InsertNoteWithMeta(ctx, rawText, durationSec, NoteMeta{})
}

// InsertNoteWithMeta inserts a note alongside its quality signals.
// Nil pointer fields are stored as NULL — distinct from a real 0.0.
// Empty AudioHash stores NULL.
func (db *DB) InsertNoteWithMeta(ctx context.Context, rawText string, durationSec int, meta NoteMeta) (int64, error) {
	var overall, worst, hash any
	if meta.ConfidenceOverall != nil {
		overall = *meta.ConfidenceOverall
	}
	if meta.ConfidenceMin != nil {
		worst = *meta.ConfidenceMin
	}
	if meta.AudioHash != "" {
		hash = meta.AudioHash
	}
	suspect := 0
	if meta.SuspectHallucination {
		suspect = 1
	}
	ts := time.Now().Unix()

	// Fast path: no dedup window requested OR no hash supplied. The
	// conditional INSERT below only earns its complexity when both are
	// set (= the bot's normal voice-message flow).
	if meta.DedupWindowSec <= 0 || meta.AudioHash == "" {
		res, err := db.ExecContext(ctx, `
			INSERT INTO notes (created_at, raw_text, duration_sec,
			                   confidence_overall, confidence_min, suspect_hallucination,
			                   audio_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			ts, rawText, durationSec, overall, worst, suspect, hash)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}

	// Race-safe dedup INSERT. SQLite's WAL mode serializes writers, so
	// two concurrent calls with the same hash see each other's rows
	// through the NOT EXISTS subquery — the second one's WHERE clause
	// evaluates to false and zero rows are inserted. We then look up
	// the surviving row and return its id with ErrDuplicateAudio.
	//
	// Stale resends (same bytes, but the older note is outside the
	// dedup window) still go through because the WHERE clause filters
	// the subquery by created_at >= cutoff.
	cutoff := ts - meta.DedupWindowSec
	res, err := db.ExecContext(ctx, `
		INSERT INTO notes (created_at, raw_text, duration_sec,
		                   confidence_overall, confidence_min, suspect_hallucination,
		                   audio_hash)
		SELECT ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM notes
			WHERE audio_hash = ? AND created_at >= ?
		)`,
		ts, rawText, durationSec, overall, worst, suspect, hash,
		meta.AudioHash, cutoff)
	if err != nil {
		return 0, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if rows == 0 {
		var existing int64
		if qerr := db.QueryRowContext(ctx,
			`SELECT id FROM notes WHERE audio_hash = ? AND created_at >= ? ORDER BY created_at DESC LIMIT 1`,
			meta.AudioHash, cutoff).Scan(&existing); qerr == nil {
			return existing, ErrDuplicateAudio
		}
		return 0, ErrDuplicateAudio
	}
	return res.LastInsertId()
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

func (db *DB) CountPending(ctx context.Context) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM notes WHERE status = 'pending'`).Scan(&n)
	return n, err
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

// GetNotesInRange returns notes whose created_at falls in [from, to),
// newest first. A non-empty status narrows the result to that status.
// limit ≤ 0 or > MaxNotesInRange is clamped to MaxNotesInRange.
func (db *DB) GetNotesInRange(ctx context.Context, from, to time.Time, status string, limit int) ([]Note, error) {
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
	return queryNotes(ctx, db, `
		SELECT id, created_at, raw_text, duration_sec, audio_path, status,
		       confidence_overall, confidence_min, suspect_hallucination
		FROM notes
		WHERE created_at >= ? AND created_at < ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, from.Unix(), to.Unix(), limit)
}

// DeleteNote permanently removes a note and returns its retained audio
// path (empty when none) so the caller can delete the on-disk file. The
// FTS5 AFTER DELETE trigger drops the search-index entry and the
// notes_history ON DELETE CASCADE FK clears the edit history in the same
// statement. Returns ErrNoteNotFound if the id is unknown. IRREVERSIBLE —
// there is no restore path.
func (db *DB) DeleteNote(ctx context.Context, id int64) (string, error) {
	var audioPath sql.NullString
	err := db.QueryRowContext(ctx, `SELECT audio_path FROM notes WHERE id = ?`, id).Scan(&audioPath)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoteNotFound
	}
	if err != nil {
		return "", err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM notes WHERE id = ?`, id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Lost a race with a concurrent delete of the same id.
		return "", ErrNoteNotFound
	}
	return audioPath.String, nil
}

// DeleteNotes permanently removes the given ids in one statement. Returns
// the retained audio paths of the deleted rows (for on-disk cleanup) and
// the number of rows actually removed. Unknown ids are silently skipped.
func (db *DB) DeleteNotes(ctx context.Context, ids []int64) ([]string, int, error) {
	if len(ids) == 0 {
		return nil, 0, nil
	}
	var paths []string
	total := 0
	// Chunk so an arbitrarily long id list stays under SQLite's bind-variable
	// limit. Per chunk we read the audio paths first, then delete; a chunk's
	// paths are appended only after its DELETE succeeds, so the returned list
	// never names a row that is still present.
	for _, chunk := range chunkIDs(ids, maxBatchIDs) {
		marks, args := idPlaceholders(chunk)
		chunkPaths, err := collectAudioPaths(ctx, db,
			fmt.Sprintf(`SELECT audio_path FROM notes WHERE audio_path IS NOT NULL AND id IN (%s)`, marks), // nosemgrep
			args)
		if err != nil {
			return paths, total, err
		}
		res, err := db.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM notes WHERE id IN (%s)`, marks), args...) // nosemgrep
		if err != nil {
			return paths, total, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
		paths = append(paths, chunkPaths...)
	}
	return paths, total, nil
}

// DeleteAllPending permanently removes every pending note in one statement.
// Returns the retained audio paths of the deleted rows and the count
// removed (0 if no pending exists).
func (db *DB) DeleteAllPending(ctx context.Context) ([]string, int, error) {
	paths, err := collectAudioPaths(ctx, db,
		`SELECT audio_path FROM notes WHERE audio_path IS NOT NULL AND status = 'pending'`, nil)
	if err != nil {
		return nil, 0, err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM notes WHERE status = 'pending'`)
	if err != nil {
		return nil, 0, err
	}
	n, _ := res.RowsAffected()
	return paths, int(n), nil
}

// collectAudioPaths runs a `SELECT audio_path ...` query and returns the
// non-empty paths. Shared by the batch delete helpers so they can hand the
// caller a list of files to remove from disk.
func collectAudioPaths(ctx context.Context, db *DB, query string, args []any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p sql.NullString
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if p.Valid && p.String != "" {
			paths = append(paths, p.String)
		}
	}
	return paths, rows.Err()
}

// MarkAnalyzed flips `pending → analyzed` for the given ids. Already-
// analyzed (or otherwise non-pending) rows are left alone and not counted.
// Returns the number of rows actually flipped.
//
// The narrow `status = 'pending'` filter is what makes this call idempotent
// under callback-storm conditions: a 50× flood of identical taps reports
// 1 + 0 + 0 + ... instead of 1 + 1 + 1 + ... because subsequent calls
// no longer match the WHERE clause. See TestMarkAnalyzed_CallbackFlood.
func (db *DB) MarkAnalyzed(ctx context.Context, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	total := 0
	for _, chunk := range chunkIDs(ids, maxBatchIDs) {
		marks, args := idPlaceholders(chunk)
		// status = 'pending' source filter keeps this idempotent under a
		// callback storm (see TestMarkAnalyzed_CallbackFlood); placeholders
		// are built from len(chunk), values bind via args — no user input
		// reaches the SQL string.
		q := fmt.Sprintf(`UPDATE notes SET status = 'analyzed' WHERE status = 'pending' AND id IN (%s)`, marks) // nosemgrep
		res, err := db.ExecContext(ctx, q, args...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// queryNotes is the shared row-scanning helper for the list/range
// queries above. New list-shaped queries should reuse it instead of
// inlining the Scan + time-conversion dance.
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
