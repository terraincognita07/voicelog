package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/terraincognita07/voicelog/internal/db"
)

// Vocabulary MCP tools. These let Claude close the transcription-quality
// loop it's uniquely positioned to see: scanning the whole corpus, it can
// notice whisper consistently mangling a name/term and add it to the
// vocabulary so future transcriptions improve. The vocabulary is appended
// to the whisper "initial prompt" (see promptbuilder).
//
// clear_vocab is deliberately NOT exposed over MCP — wiping the whole list
// is a destructive, human-intent action kept to the bot's two-step confirm.

// maxVocabTermLen caps a single term so an agent can't push a pathological
// long string that would dominate the bounded whisper prompt. Mirrors the
// bot-side limit (internal/telegram.MaxVocabTermLen); duplicated rather than
// imported to avoid an mcp→telegram dependency.
const maxVocabTermLen = 64

func registerListVocab(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("list_vocab",
		mcpsdk.WithDescription("List the current whisper vocabulary terms (names, jargon, rare "+
			"words) that bias transcription. Newest first. Use this before add_vocab to avoid "+
			"duplicates and to see what's already biasing the decoder."),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
	)
	s.AddTool(tool, func(ctx context.Context, _ mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		terms, err := store.ListVocab(ctx)
		if err != nil {
			logger.Error("list_vocab", "err", err)
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"terms": terms, "count": len(terms)})
	})
}

func registerAddVocab(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("add_vocab",
		mcpsdk.WithDescription("Add one or more terms to the whisper vocabulary so future "+
			"transcriptions recognize them. Use when you notice whisper repeatedly mis-spelling "+
			"the same name or jargon across notes. Case is preserved (store 'Иннокентий', not "+
			"'иннокентий'); duplicates are ignored case-insensitively. Terms longer than "+
			fmt.Sprintf("%d characters are skipped. Returns {added, skipped_existing, skipped_too_long}.", maxVocabTermLen)),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithArray("terms", mcpsdk.Required(),
			mcpsdk.Description("Terms to add (each ≤"+fmt.Sprintf("%d", maxVocabTermLen)+" chars)."),
			mcpsdk.Items(map[string]any{"type": "string"}),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		terms, err := toStringSlice(req.GetArguments()["terms"])
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		added, existing, tooLong := 0, 0, 0
		for _, raw := range terms {
			t := strings.TrimSpace(raw)
			if t == "" {
				continue
			}
			if len([]rune(t)) > maxVocabTermLen {
				tooLong++
				continue
			}
			ok, aerr := store.AddVocab(ctx, t)
			if aerr != nil {
				logger.Error("add_vocab", "err", aerr)
				return mcpsdk.NewToolResultError(aerr.Error()), nil
			}
			if ok {
				added++
			} else {
				existing++
			}
		}
		return jsonResult(map[string]int{
			"added":            added,
			"skipped_existing": existing,
			"skipped_too_long": tooLong,
		})
	})
}

func registerRemoveVocab(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("remove_vocab",
		mcpsdk.WithDescription("Remove a single term from the whisper vocabulary (case-insensitive "+
			"match). Returns {removed: bool} — false if the term wasn't present. To wipe the whole "+
			"vocabulary, use the bot's /vocab clear (not exposed over MCP by design)."),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithString("term", mcpsdk.Required(),
			mcpsdk.Description("The term to remove."),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		term, err := req.RequireString("term")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		removed, rerr := store.RemoveVocab(ctx, term)
		if rerr != nil {
			logger.Error("remove_vocab", "err", rerr)
			return mcpsdk.NewToolResultError(rerr.Error()), nil
		}
		return jsonResult(map[string]bool{"removed": removed})
	})
}

// toStringSlice converts a JSON-decoded `terms` argument into []string.
func toStringSlice(v any) ([]string, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("terms must be an array")
	}
	out := make([]string, 0, len(arr))
	for i, x := range arr {
		s, ok := x.(string)
		if !ok {
			return nil, fmt.Errorf("terms[%d]: want string, got %T", i, x)
		}
		out = append(out, s)
	}
	return out, nil
}
