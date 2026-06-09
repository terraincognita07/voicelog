package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AudioRef pairs a note id with its retained audio file path. Used by
// the janitor and the F3 startup tasks (RelativizeLegacyPaths,
// ScanOrphans) to enumerate retained audio without pulling the full
// Note struct.
type AudioRef struct {
	ID   int64
	Path string
}

// SetAudioPath stores the on-disk path of the retained audio file for
// the given note. No-op-safe: setting twice overwrites. An empty path
// clears the field to NULL (via ClearAudioPath) rather than writing an
// empty string, so the janitor's `audio_path IS NOT NULL` worklist never
// picks up a phantom zero-length path.
func (db *DB) SetAudioPath(ctx context.Context, id int64, path string) error {
	if path == "" {
		return db.ClearAudioPath(ctx, id)
	}
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

// AllRetainedAudios returns every (id, path) pair where audio_path IS
// NOT NULL — no time filter. Used by startup tasks (F3 legacy path
// normalize, orphan scan) that need to enumerate ALL retained audio,
// not just the janitor's cleanup window.
func (db *DB) AllRetainedAudios(ctx context.Context) ([]AudioRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, audio_path FROM notes
		WHERE audio_path IS NOT NULL
		ORDER BY id ASC`)
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

// DupNote describes an existing note that matches a recent audio hash —
// returned by FindRecentByHash so the caller can quote "duplicate of #N
// from X seconds ago" in the user reply.
type DupNote struct {
	ID        int64
	CreatedAt time.Time
}

// FindRecentByHash looks for a note with the given audio_hash created
// within `window` of now. Returns ErrNoteNotFound when nothing matches —
// distinct from a real DB error so the caller can treat "no dup" as a
// happy path.
func (db *DB) FindRecentByHash(ctx context.Context, hash string, window time.Duration) (DupNote, error) {
	if hash == "" {
		return DupNote{}, ErrNoteNotFound
	}
	cutoff := time.Now().Add(-window).Unix()
	var (
		id int64
		ts int64
	)
	err := db.QueryRowContext(ctx, `
		SELECT id, created_at FROM notes
		WHERE audio_hash = ? AND created_at >= ?
		ORDER BY created_at DESC
		LIMIT 1`, hash, cutoff).Scan(&id, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return DupNote{}, ErrNoteNotFound
	}
	if err != nil {
		return DupNote{}, err
	}
	return DupNote{ID: id, CreatedAt: time.Unix(ts, 0)}, nil
}
