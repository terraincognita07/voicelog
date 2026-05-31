package db

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/kljensen/snowball/russian"
)

// NoteWithRank is what SearchNotes returns per hit — the underlying
// Note plus the FTS5 bm25 rank (lower is better) and a snippet of
// surrounding text with the matched term wrapped in `<<` / `>>`.
type NoteWithRank struct {
	Note
	Rank    float64
	Snippet string
}

// stemCyrillicQuery rewrites each bare Cyrillic word in an FTS5 query into a
// stemmed prefix match. The Snowball Russian stemmer strips the inflectional
// suffix down to the stem ("работа"/"работе"/"работу" → "работ"), and the
// trailing '*' turns the stem into an FTS5 prefix query so it matches every
// inflected form stored in the default unicode61 index. Without this, the
// tokenizer treats each grammatical form as an unrelated token and a search
// for the dictionary form returns nothing.
//
// Deliberately conservative — a token is rewritten ONLY when it is a bare
// word (letters/digits/underscore) containing at least one Cyrillic letter
// and not already a wildcard. Left untouched:
//   - Latin terms (English precision unchanged — "cat" never becomes "cat*")
//   - FTS5 operators (OR / AND / NOT / NEAR — Latin, no Cyrillic)
//   - quoted phrases, column filters, parens, existing wildcards (any token
//     carrying a non-word rune is passed through verbatim)
//
// Query-side only: the index stays plain unicode61, so this needs no
// migration and no re-index. The trade is recall over precision for Cyrillic
// (a short stem may over-match), which is the right direction for a journal.
func stemCyrillicQuery(query string) string {
	fields := strings.Fields(query)
	for i, f := range fields {
		if !isBareCyrillicWord(f) {
			continue
		}
		stem := russian.Stem(strings.ToLower(f), false)
		if stem == "" {
			continue
		}
		fields[i] = stem + "*"
	}
	return strings.Join(fields, " ")
}

func isBareCyrillicWord(s string) bool {
	if s == "" || strings.HasSuffix(s, "*") {
		return false
	}
	hasCyrillic := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Cyrillic, r):
			hasCyrillic = true
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_':
			// allowed inside a bare word, but not Cyrillic on its own
		default:
			return false // punctuation, quote, operator glyph, etc.
		}
	}
	return hasCyrillic
}

// SearchNotes runs an FTS5 MATCH and returns rows ordered by bm25 rank
// (lower rank = better match). query is passed through to FTS5 as-is, so it
// supports the full FTS5 query syntax (phrases in quotes, OR, NEAR, *).
func (db *DB) SearchNotes(ctx context.Context, query string, limit int) ([]NoteWithRank, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("empty query")
	}
	query = stemCyrillicQuery(query)
	rows, err := db.QueryContext(ctx, `
		SELECT n.id, n.created_at, n.raw_text, n.duration_sec, n.audio_path, n.status,
		       n.confidence_overall, n.confidence_min, n.suspect_hallucination,
		       bm25(notes_fts) AS rank,
		       snippet(notes_fts, 0, '<<', '>>', '...', 30) AS snip
		FROM notes_fts
		JOIN notes n ON n.id = notes_fts.rowid
		WHERE notes_fts MATCH ?
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
