// Package promptbuilder assembles the whisper "initial prompt" from
// the operator-set basePrompt and the user-managed vocabulary stored
// in the DB. Lives in its own package because two callers — the bot
// (for live transcription) and the MCP retranscribe tool — both need
// the same composition rules.
package promptbuilder

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// vocabTimeout caps how long we wait on the DB lookup. The prompt is
// non-critical — if VocabPrompt is slow or fails, we ship basePrompt
// alone rather than blocking the transcription on it.
const vocabTimeout = 2 * time.Second

// VocabSource is the minimum contract Compose needs from its data
// store. *db.DB implements it implicitly via VocabPrompt — declaring
// it as an interface here keeps promptbuilder free of any direct
// dependency on internal/db and makes the function trivially mockable
// in tests.
type VocabSource interface {
	VocabPrompt(ctx context.Context) (string, error)
}

// Compose returns basePrompt + " " + vocabulary, with these rules:
//   - basePrompt is TrimSpaced before use.
//   - VocabPrompt errors are non-fatal; if logger != nil, the error
//     is recorded at Warn level. The function still returns
//     basePrompt alone in that case.
//   - Returns "" iff both basePrompt and vocabulary are empty.
//
// Callers pass their own *slog.Logger so the message origin is visible
// in structured logs ("vocab prompt" entries from the bot vs the MCP).
func Compose(ctx context.Context, src VocabSource, basePrompt string, logger *slog.Logger) string {
	dbCtx, cancel := context.WithTimeout(ctx, vocabTimeout)
	defer cancel()
	vocab, err := src.VocabPrompt(dbCtx)
	if err != nil && logger != nil {
		logger.Warn("vocab prompt", "err", err)
	}
	base := strings.TrimSpace(basePrompt)
	switch {
	case base == "" && vocab == "":
		return ""
	case base == "":
		return vocab
	case vocab == "":
		return base
	default:
		return base + " " + vocab
	}
}
