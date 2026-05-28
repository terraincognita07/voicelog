package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"voicelog/internal/db"
	"voicelog/internal/db/migrations"
	"voicelog/internal/mcp"
	"voicelog/internal/whisper"
)

const testToken = "abcdefghijklmnopqrst"

// rpcEnvelope is the JSON-RPC 2.0 wrapper MCP responses live inside.
type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// toolCallResult is the shape mcp-go returns inside `result` for
// tools/call. Each content entry's text is a JSON string we then decode
// to the actual payload.
type toolCallResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// fixture wires up an in-memory MCP server backed by a fresh SQLite
// file and returns the URL of an httptest server that's already
// guarded by BearerAuth.
type fixture struct {
	url   string
	store *db.DB
}

func newFixture(t *testing.T) fixture {
	return newFixtureWith(t, mcp.RetranscribeDeps{})
}

func newFixtureWith(t *testing.T, deps mcp.RetranscribeDeps) fixture {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := mcp.NewServer(store, deps, logger)
	httpHandler := mcpserver.NewStreamableHTTPServer(srv,
		mcpserver.WithEndpointPath("/mcp"),
		mcpserver.WithStateLess(true),
	)
	ts := httptest.NewServer(mcp.BearerAuth(testToken, httpHandler))
	t.Cleanup(ts.Close)
	return fixture{url: ts.URL, store: store}
}

// rawPost sends a raw byte body to /mcp with an optional bearer token
// and returns the HTTP response.
func rawPost(t *testing.T, baseURL, token string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	return resp
}

// callTool issues a tools/call JSON-RPC request and returns the
// decoded toolCallResult. Asserts HTTP 200 + JSON-RPC success along
// the way. The Accept header includes text/event-stream because the
// mcp-go streamable transport may answer with SSE; readSingleEnvelope
// handles both transports.
func callTool(t *testing.T, f fixture, name string, args map[string]any) toolCallResult {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	})
	resp := rawPost(t, f.url, testToken, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("tools/call status %d, body=%s", resp.StatusCode, raw)
	}
	env := readSingleEnvelope(t, resp)
	if env.Error != nil {
		t.Fatalf("rpc error for %s: %s", name, env.Error.Message)
	}
	var tcr toolCallResult
	if err := json.Unmarshal(env.Result, &tcr); err != nil {
		t.Fatalf("decode tool result: %v (raw=%s)", err, env.Result)
	}
	return tcr
}

// readSingleEnvelope handles both content-types the mcp-go transport
// can produce: a plain application/json body OR a single SSE event
// (event: message\ndata: {...}\n\n). One envelope per call.
func readSingleEnvelope(t *testing.T, resp *http.Response) rpcEnvelope {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	ct := resp.Header.Get("Content-Type")
	body := string(raw)
	if strings.HasPrefix(ct, "text/event-stream") {
		// Pull the first `data: ...` line — that's the JSON-RPC payload.
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				body = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				break
			}
		}
	}
	var env rpcEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode envelope: %v (raw=%q)", err, raw)
	}
	return env
}

// decodePayload unmarshals the JSON-encoded text in result.Content[0]
// into out. Most tools return a single text content with a JSON string.
func decodePayload(t *testing.T, tcr toolCallResult, out any) {
	t.Helper()
	if len(tcr.Content) == 0 {
		t.Fatalf("empty content in tool result")
	}
	first := tcr.Content[0]
	if first.Type != "text" {
		t.Fatalf("content[0].type = %q, want text", first.Type)
	}
	if err := json.Unmarshal([]byte(first.Text), out); err != nil {
		t.Fatalf("decode payload: %v (text=%q)", err, first.Text)
	}
}

// expectToolError asserts the call surfaced as an MCP tool-level error
// and returns the human-readable message for substring assertions.
func expectToolError(t *testing.T, tcr toolCallResult) string {
	t.Helper()
	if !tcr.IsError {
		t.Fatalf("expected tool error, got success: %+v", tcr)
	}
	if len(tcr.Content) == 0 {
		t.Fatalf("tool error has no content")
	}
	return tcr.Content[0].Text
}

// seedNote inserts a note with controlled created_at and returns the
// new note id. Used by all tools that touch existing rows.
func seedNote(t *testing.T, store *db.DB, when time.Time, text string) int64 {
	t.Helper()
	res, err := store.ExecContext(context.Background(),
		`INSERT INTO notes (created_at, raw_text, duration_sec) VALUES (?, ?, ?)`,
		when.Unix(), text, 3)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// --- Auth ---------------------------------------------------------------

func TestBearerAuth_RejectsMissingHeader(t *testing.T) {
	f := newFixture(t)
	resp := rawPost(t, f.url, "", []byte(`{}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: want 401, got %d", resp.StatusCode)
	}
}

func TestBearerAuth_RejectsWrongToken(t *testing.T) {
	f := newFixture(t)
	resp := rawPost(t, f.url, "wrong-token-zzz", []byte(`{}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: want 401, got %d", resp.StatusCode)
	}
}

func TestBearerAuth_AcceptsCorrectToken(t *testing.T) {
	f := newFixture(t)
	// Empty tools/call would fail at the protocol level but auth must
	// at least let it through to the server (status 200 with JSON-RPC
	// error body, not 401).
	resp := rawPost(t, f.url, testToken, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("auth rejected correct token: %d", resp.StatusCode)
	}
}

// --- Tools (happy paths) ------------------------------------------------

func TestListPendingNotes(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	id := seedNote(t, f.store, now.Add(-1*time.Minute), "remember to buy milk")
	seedNote(t, f.store, now.Add(-2*time.Minute), "older pending")

	tcr := callTool(t, f, "list_pending_notes", map[string]any{"limit": 10})
	var got []map[string]any
	decodePayload(t, tcr, &got)
	if len(got) != 2 {
		t.Fatalf("notes count: want 2, got %d", len(got))
	}
	// Newest first.
	if int64(got[0]["id"].(float64)) != id {
		t.Errorf("first note id: want %d, got %v", id, got[0]["id"])
	}
}

func TestGetNotesInRange(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	id := seedNote(t, f.store, now.Add(-1*time.Hour), "inside range")
	seedNote(t, f.store, now.Add(-48*time.Hour), "outside range")

	from := now.Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	to := now.Add(1 * time.Hour).UTC().Format(time.RFC3339)
	tcr := callTool(t, f, "get_notes_in_range", map[string]any{
		"from": from,
		"to":   to,
	})
	var got []map[string]any
	decodePayload(t, tcr, &got)
	if len(got) != 1 {
		t.Fatalf("want 1 in range, got %d", len(got))
	}
	if int64(got[0]["id"].(float64)) != id {
		t.Errorf("id: want %d, got %v", id, got[0]["id"])
	}
}

func TestSearchNotes(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	hit := seedNote(t, f.store, now, "купить молоко завтра")
	seedNote(t, f.store, now, "позвонить маме")

	tcr := callTool(t, f, "search_notes", map[string]any{"query": "молоко"})
	var got []map[string]any
	decodePayload(t, tcr, &got)
	if len(got) != 1 {
		t.Fatalf("want 1 hit, got %d", len(got))
	}
	if int64(got[0]["id"].(float64)) != hit {
		t.Errorf("hit id: want %d, got %v", hit, got[0]["id"])
	}
	if got[0]["snippet"] == nil || got[0]["snippet"].(string) == "" {
		t.Errorf("snippet should be non-empty")
	}
}

func TestGetNote_Found(t *testing.T) {
	f := newFixture(t)
	id := seedNote(t, f.store, time.Now(), "single note")
	tcr := callTool(t, f, "get_note", map[string]any{"id": float64(id)})
	var got map[string]any
	decodePayload(t, tcr, &got)
	if int64(got["id"].(float64)) != id {
		t.Errorf("id: want %d, got %v", id, got["id"])
	}
	if got["raw_text"].(string) != "single note" {
		t.Errorf("raw_text: %v", got["raw_text"])
	}
}

func TestGetNote_NotFound(t *testing.T) {
	f := newFixture(t)
	tcr := callTool(t, f, "get_note", map[string]any{"id": float64(99999)})
	msg := expectToolError(t, tcr)
	if !strings.Contains(msg, "not found") {
		t.Errorf("error message: %q", msg)
	}
}

func TestMarkAnalyzed(t *testing.T) {
	f := newFixture(t)
	id1 := seedNote(t, f.store, time.Now(), "a")
	id2 := seedNote(t, f.store, time.Now(), "b")

	tcr := callTool(t, f, "mark_analyzed", map[string]any{
		"ids": []any{id1, id2},
	})
	var got map[string]int
	decodePayload(t, tcr, &got)
	if got["updated"] != 2 {
		t.Errorf("updated: want 2, got %d", got["updated"])
	}

	// Verify the rows are actually analyzed now.
	n, _ := f.store.GetNote(context.Background(), id1)
	if string(n.Status) != "analyzed" {
		t.Errorf("note %d status = %q", id1, n.Status)
	}
}

func TestDiscardNotes(t *testing.T) {
	f := newFixture(t)
	id := seedNote(t, f.store, time.Now(), "discard me")

	tcr := callTool(t, f, "discard_notes", map[string]any{
		"ids": []any{id},
	})
	var got map[string]int
	decodePayload(t, tcr, &got)
	if got["updated"] != 1 {
		t.Errorf("updated: want 1, got %d", got["updated"])
	}
	n, _ := f.store.GetNote(context.Background(), id)
	if string(n.Status) != "discarded" {
		t.Errorf("status = %q", n.Status)
	}
}

func TestRestoreNote_DiscardedToPending(t *testing.T) {
	f := newFixture(t)
	id := seedNote(t, f.store, time.Now(), "restore me")
	if _, err := f.store.DiscardNotes(context.Background(), []int64{id}); err != nil {
		t.Fatalf("discard: %v", err)
	}

	tcr := callTool(t, f, "restore_note", map[string]any{"id": float64(id)})
	var got map[string]bool
	decodePayload(t, tcr, &got)
	if !got["restored"] {
		t.Errorf("restored: want true, got false")
	}
	n, _ := f.store.GetNote(context.Background(), id)
	if string(n.Status) != "pending" {
		t.Errorf("status after restore: %q", n.Status)
	}
}

func TestRestoreNote_AnalyzedNotRestorable(t *testing.T) {
	f := newFixture(t)
	id := seedNote(t, f.store, time.Now(), "analyzed note")
	if _, err := f.store.MarkAnalyzed(context.Background(), []int64{id}); err != nil {
		t.Fatalf("mark analyzed: %v", err)
	}

	tcr := callTool(t, f, "restore_note", map[string]any{"id": float64(id)})
	var got map[string]bool
	decodePayload(t, tcr, &got)
	// analyzed → pending is not a permitted transition; restore returns
	// {restored: false} rather than erroring.
	if got["restored"] {
		t.Errorf("restored: want false for analyzed note, got true")
	}
}

func TestRetranscribe_UnavailableWithoutWhisper(t *testing.T) {
	// Default fixture leaves RetranscribeDeps.Whisper == nil. Tool must
	// reply with a clear "unavailable" message instead of crashing.
	f := newFixture(t)
	id := seedNote(t, f.store, time.Now(), "any text")
	tcr := callTool(t, f, "retranscribe", map[string]any{"id": float64(id)})
	msg := expectToolError(t, tcr)
	if !strings.Contains(strings.ToLower(msg), "unavailable") {
		t.Errorf("error should mention 'unavailable': %q", msg)
	}
}

func TestRetranscribe_RefusesDiscarded(t *testing.T) {
	// Wire a non-nil Whisper client so the "unavailable" branch
	// doesn't short-circuit before the discard check. The client is
	// never actually called — the discarded guard returns first.
	deps := mcp.RetranscribeDeps{Whisper: &whisper.Client{}}
	f := newFixtureWith(t, deps)
	id := seedNote(t, f.store, time.Now(), "to be discarded")
	if _, err := f.store.DiscardNotes(context.Background(), []int64{id}); err != nil {
		t.Fatalf("discard: %v", err)
	}

	tcr := callTool(t, f, "retranscribe", map[string]any{"id": float64(id)})
	msg := expectToolError(t, tcr)
	if !strings.Contains(strings.ToLower(msg), "discarded") {
		t.Errorf("error should mention 'discarded', got %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "restore") {
		t.Errorf("error should hint at restore_note, got %q", msg)
	}
}

func TestDBHealth(t *testing.T) {
	f := newFixture(t)
	tcr := callTool(t, f, "db_health", nil)
	var got map[string]any
	decodePayload(t, tcr, &got)
	// integrity_check / quick_check both return "ok" on a healthy DB.
	if got["integrity_check"] != "ok" {
		t.Errorf("integrity_check: want %q, got %v", "ok", got["integrity_check"])
	}
	if got["quick_check"] != "ok" {
		t.Errorf("quick_check: want %q, got %v", "ok", got["quick_check"])
	}
}

// --- Sanity ------------------------------------------------------------

func TestUnauthorizedHasWWWAuthenticate(t *testing.T) {
	f := newFixture(t)
	resp := rawPost(t, f.url, "", []byte(`{}`))
	defer resp.Body.Close()
	wa := resp.Header.Get("WWW-Authenticate")
	if !strings.Contains(wa, "Bearer") {
		t.Errorf("WWW-Authenticate: want Bearer realm, got %q", wa)
	}
}
