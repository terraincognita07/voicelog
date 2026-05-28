package db

import (
	"context"
	"errors"
	"strings"
	"time"
)

// NoteWithRank is what SearchNotes returns per hit — the underlying
// Note plus the FTS5 bm25 rank (lower is better) and a snippet of
// surrounding text with the matched term wrapped in `<<` / `>>`.
type NoteWithRank struct {
	Note
	Rank    float64
	Snippet string
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
