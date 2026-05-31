package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/terraincognita07/voicelog/internal/db"
)

// Tag MCP tools. Tags are an analysis-side overlay: Claude labels notes with
// categories that are NOT in the note's own words (#идея, #todo, #философия),
// giving a deterministic counterpart to full-text search. The rule of thumb
// (encoded in the tool descriptions) is "tag the judgment, not the words" —
// a tag that merely repeats a word already in the text duplicates search and
// earns nothing.

func registerTagNote(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("tag_note",
		mcpsdk.WithDescription("Attach one or more category tags to a note. A tag is a short label "+
			"for a JUDGMENT that isn't in the note's own words — #идея, #todo, #важное, #философия — "+
			"so tags complement full-text search instead of duplicating it. Don't tag a note "+
			"'#работа' just because it contains the word 'работа' (search already finds that); tag "+
			"what the words don't say. Tags are normalized (lowercased, a leading '#' dropped, ≤64 "+
			"chars); duplicates on the same note are ignored. A typical pass adds 2-3 tags. Returns "+
			"{added: N}."),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithNumber("id", mcpsdk.Required(),
			mcpsdk.Description("Note ID to tag."),
		),
		mcpsdk.WithArray("tags", mcpsdk.Required(),
			mcpsdk.Description("Tags to attach (with or without a leading '#')."),
			mcpsdk.Items(map[string]any{"type": "string"}),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		idF, err := req.RequireFloat("id")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		tags, err := toStringSlice(req.GetArguments()["tags"])
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		n, err := store.AddTags(ctx, int64(idF), tags)
		if err != nil {
			if errors.Is(err, db.ErrNoteNotFound) {
				return mcpsdk.NewToolResultError(fmt.Sprintf("note %d not found", int64(idF))), nil
			}
			logger.Error("tag_note", "err", err)
			return opFailed("tag_note"), nil
		}
		return jsonResult(map[string]int{"added": n})
	})
}

func registerUntagNote(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("untag_note",
		mcpsdk.WithDescription("Remove a single tag from a note. The tag is normalized the same way "+
			"as tag_note, so '#Идея' and 'идея' both match. Returns {removed: bool} — false if the "+
			"note didn't carry that tag."),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithNumber("id", mcpsdk.Required(),
			mcpsdk.Description("Note ID."),
		),
		mcpsdk.WithString("tag", mcpsdk.Required(),
			mcpsdk.Description("The tag to remove (with or without a leading '#')."),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		idF, err := req.RequireFloat("id")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		tag, err := req.RequireString("tag")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		ok, err := store.RemoveTag(ctx, int64(idF), tag)
		if err != nil {
			logger.Error("untag_note", "err", err)
			return opFailed("untag_note"), nil
		}
		return jsonResult(map[string]bool{"removed": ok})
	})
}

func registerListTags(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("list_tags",
		mcpsdk.WithDescription("List every tag in use with the number of notes carrying it, "+
			"most-used first. Use it to see the category vocabulary before tagging (reuse an "+
			"existing tag instead of coining a near-duplicate) or to answer 'what have I been "+
			"tagging lately?'. Returns [{tag, count}]."),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
	)
	s.AddTool(tool, func(ctx context.Context, _ mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		tags, err := store.ListTags(ctx)
		if err != nil {
			logger.Error("list_tags", "err", err)
			return opFailed("list_tags"), nil
		}
		return jsonResult(tags)
	})
}

func registerNotesByTag(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("notes_by_tag",
		mcpsdk.WithDescription("List notes carrying a given tag, newest first — the deterministic "+
			"counterpart to search_notes. It returns exactly the notes labeled with the tag, "+
			"regardless of wording: notes_by_tag('философия') surfaces every philosophical musing "+
			"even when the word 'философия' never appears in them. The tag is normalized "+
			"('#Идея' == 'идея'). Each note carries its full tag list."),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithString("tag", mcpsdk.Required(),
			mcpsdk.Description("The tag to filter by (with or without a leading '#')."),
		),
		mcpsdk.WithNumber("limit",
			mcpsdk.Description(fmt.Sprintf("Maximum notes to return. Default and hard cap %d.", db.MaxNotesInRange)),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		tag, err := req.RequireString("tag")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		limit := 0
		if v, err := req.RequireFloat("limit"); err == nil && v > 0 {
			limit = int(v)
		}
		notes, err := store.NotesByTag(ctx, tag, limit)
		if err != nil {
			logger.Error("notes_by_tag", "tag", tag, "err", err)
			return opFailed("notes_by_tag"), nil
		}
		out := make([]mcpNote, len(notes))
		for i, n := range notes {
			out[i] = toMCP(n)
		}
		if err := attachTags(ctx, store, out); err != nil {
			logger.Warn("notes_by_tag: attach tags", "err", err)
		}
		return jsonResult(out)
	})
}
