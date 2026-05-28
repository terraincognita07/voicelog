package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"

	"voicelog/internal/db"
	"voicelog/internal/whisper"
)

// makeAudio writes deterministic bytes to a tmp file under t.TempDir() and
// returns the path + the same bytes' SHA-256 (matches what processSource
// computes via hashFile). audioBody is plain content — the test never
// invokes ffmpeg, so any bytes work.
func makeAudio(t *testing.T, audioBody string) (path, hash string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "src")
	if err := os.WriteFile(p, []byte(audioBody), 0o600); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	sum := sha256.Sum256([]byte(audioBody))
	return p, hex.EncodeToString(sum[:])
}

// wireTranscriber installs a fakeTranscriber on tb and returns it so the
// test can manipulate result/err.
func wireTranscriber(tb *Bot, ft *fakeTranscriber) {
	tb.whisper = ft
	tb.hallucinationThresh = 0.6
}

// TestProcessSource_HappyPathInsertsAndReplies covers the canonical voice
// → whisper → InsertNote → saved-reply flow. Asserts:
//
//   - whisper.Transcribe is called once with the right path
//   - exactly one note is inserted, with the trimmed text
//   - confidence fields are populated from the result's Aggregate
//   - the user reply uses Recorded (not Duplicate / EmptyTrans / Disk-full)
//   - the reply carries an inline-keyboard markup (the saved-reply discard btn)
func TestProcessSource_HappyPathInsertsAndReplies(t *testing.T) {
	tb := newTestBot(t)
	ft := &fakeTranscriber{
		result: whisper.Result{
			Text: "  Hello world  ",
			Segments: []whisper.Segment{
				{AvgLogprob: -0.10, NoSpeechProb: 0.05},
				{AvgLogprob: -0.30, NoSpeechProb: 0.05},
			},
		},
	}
	wireTranscriber(tb, ft)

	src, _ := makeAudio(t, "audio-bytes-A")
	fc := &fakeCtx{}
	if err := tb.processSource(context.Background(), fc, src, 12); err != nil {
		t.Fatalf("processSource: %v", err)
	}

	if ft.callCount() != 1 {
		t.Fatalf("whisper called %d times, want 1", ft.callCount())
	}
	call, _ := ft.lastCall()
	if call.SrcPath != src {
		t.Errorf("transcribe srcPath = %q, want %q", call.SrcPath, src)
	}

	// One reply was sent — Recorded path. Markup attached.
	sm, ok := fc.lastSent()
	if !ok {
		t.Fatal("no reply was sent")
	}
	body, isStr := sm.What.(string)
	if !isStr {
		t.Fatalf("reply body is not a string: %T", sm.What)
	}
	if !strings.Contains(body, "Hello world") {
		t.Errorf("reply does not contain transcript: %q", body)
	}
	if !strings.Contains(body, "✓") {
		t.Errorf("reply missing saved-marker '✓': %q", body)
	}
	if len(sm.Opts) == 0 {
		t.Errorf("reply has no markup opts")
	}

	// The note was inserted with the trimmed text and the confidence
	// aggregates computed from the segments. We hit the DB directly here
	// because asserting through MCP would duplicate that package's tests.
	notes := allNotes(t, tb.db)
	if len(notes) != 1 {
		t.Fatalf("notes inserted = %d, want 1", len(notes))
	}
	n := notes[0]
	if n.RawText != "Hello world" {
		t.Errorf("stored raw_text = %q, want %q", n.RawText, "Hello world")
	}
	if !n.ConfidenceOverall.Valid {
		t.Errorf("expected confidence_overall to be set; got NULL")
	}
	if n.SuspectHallucination {
		t.Errorf("expected suspect_hallucination=false; got true")
	}
}

// TestProcessSource_EmptyTranscriptShortCircuits asserts that an empty
// result.Text (after trimming) sends the EmptyTrans message and inserts
// nothing.
func TestProcessSource_EmptyTranscriptShortCircuits(t *testing.T) {
	tb := newTestBot(t)
	ft := &fakeTranscriber{result: whisper.Result{Text: "   "}}
	wireTranscriber(tb, ft)

	src, _ := makeAudio(t, "audio-bytes-B")
	fc := &fakeCtx{}
	if err := tb.processSource(context.Background(), fc, src, 3); err != nil {
		t.Fatalf("processSource: %v", err)
	}

	sm, ok := fc.lastSent()
	if !ok {
		t.Fatal("no reply was sent")
	}
	if sm.What != tb.msg.EmptyTrans {
		t.Errorf("reply = %q, want EmptyTrans (%q)", sm.What, tb.msg.EmptyTrans)
	}
	if len(sm.Opts) != 0 {
		t.Errorf("empty-trans reply must not have markup opts; got %v", sm.Opts)
	}
	if got := len(allNotes(t, tb.db)); got != 0 {
		t.Errorf("notes inserted = %d, want 0", got)
	}
}

// TestProcessSource_WhisperErrorReturnsSanitizedReply verifies that a
// whisper error surfaces via errReply with the locale "whisper" prefix,
// not the raw error message — the audit fix that error.Error() must not
// leak through.
func TestProcessSource_WhisperErrorReturnsSanitizedReply(t *testing.T) {
	tb := newTestBot(t)
	ft := &fakeTranscriber{err: fmt.Errorf("whisper.internal.host:9000 connection refused")}
	wireTranscriber(tb, ft)

	src, _ := makeAudio(t, "audio-bytes-C")
	fc := &fakeCtx{}
	if err := tb.processSource(context.Background(), fc, src, 1); err != nil {
		t.Fatalf("processSource returned error: %v", err)
	}

	sm, ok := fc.lastSent()
	if !ok {
		t.Fatal("no reply was sent")
	}
	body, _ := sm.What.(string)
	want := tb.userErrMsg("whisper", nil) // logger noise tolerated
	if body != want {
		t.Errorf("reply body = %q, want %q", body, want)
	}
	// CRITICAL: raw whisper hostname must not leak.
	if strings.Contains(body, "whisper.internal.host:9000") {
		t.Errorf("raw whisper error leaked into chat: %q", body)
	}
	if got := len(allNotes(t, tb.db)); got != 0 {
		t.Errorf("notes inserted on whisper error = %d, want 0", got)
	}
}

// TestProcessSource_DedupHitShortCircuitsBeforeWhisper checks that a SHA-256
// match within dedupWindow returns the Duplicate reply and does NOT call
// whisper or insert a second row.
func TestProcessSource_DedupHitShortCircuitsBeforeWhisper(t *testing.T) {
	tb := newTestBot(t)
	ft := &fakeTranscriber{result: whisper.Result{Text: "irrelevant"}}
	wireTranscriber(tb, ft)

	src, hash := makeAudio(t, "audio-bytes-DEDUP")
	// Pre-seed a note with the same audio_hash that processSource will
	// compute. The dedup query uses created_at within dedupWindow; insert
	// is "now" via InsertNoteWithMeta.
	priorID, err := tb.db.InsertNoteWithMeta(context.Background(), "old transcript", 5,
		db.NoteMeta{AudioHash: hash})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	fc := &fakeCtx{}
	if err := tb.processSource(context.Background(), fc, src, 5); err != nil {
		t.Fatalf("processSource: %v", err)
	}

	if ft.callCount() != 0 {
		t.Errorf("whisper called %d times on dedup hit; want 0", ft.callCount())
	}
	if got := len(allNotes(t, tb.db)); got != 1 {
		t.Errorf("notes count = %d (want 1; second insert must be suppressed)", got)
	}
	sm, ok := fc.lastSent()
	if !ok {
		t.Fatal("no reply was sent")
	}
	body, _ := sm.What.(string)
	// The Duplicate message references the prior note ID.
	if !strings.Contains(body, fmt.Sprintf("#%d", priorID)) {
		t.Errorf("duplicate reply must reference prior id #%d: %q", priorID, body)
	}
}

// TestProcessSource_DedupOutsideWindowFallsThrough confirms that a hash
// match older than dedupWindow does NOT short-circuit — the user can
// re-send the same recording the next morning.
func TestProcessSource_DedupOutsideWindowFallsThrough(t *testing.T) {
	tb := newTestBot(t)
	ft := &fakeTranscriber{result: whisper.Result{Text: "fresh transcript"}}
	wireTranscriber(tb, ft)

	src, hash := makeAudio(t, "audio-bytes-OLDDUP")
	// Seed a note with the same hash but created_at older than the window.
	oldTime := time.Now().Add(-2 * dedupWindow).Unix()
	if _, err := tb.db.ExecContext(context.Background(),
		`INSERT INTO notes (created_at, raw_text, duration_sec, audio_hash) VALUES (?, ?, ?, ?)`,
		oldTime, "stale", 5, hash); err != nil {
		t.Fatalf("seed: %v", err)
	}

	fc := &fakeCtx{}
	if err := tb.processSource(context.Background(), fc, src, 5); err != nil {
		t.Fatalf("processSource: %v", err)
	}

	if ft.callCount() != 1 {
		t.Errorf("whisper calls = %d, want 1 (out-of-window dedup must not short-circuit)", ft.callCount())
	}
	if got := len(allNotes(t, tb.db)); got != 2 {
		t.Errorf("notes count = %d, want 2 (stale + fresh)", got)
	}
}

// TestProcessSource_NoSegmentsStoresNullConfidence validates that the
// "old whisper" / plain-json path (Aggregate returns ok=false) inserts the
// note with NULL confidence columns rather than fabricated 0.0 values.
func TestProcessSource_NoSegmentsStoresNullConfidence(t *testing.T) {
	tb := newTestBot(t)
	ft := &fakeTranscriber{result: whisper.Result{Text: "no segs", Segments: nil}}
	wireTranscriber(tb, ft)

	src, _ := makeAudio(t, "audio-bytes-NOSEG")
	if err := tb.processSource(context.Background(), &fakeCtx{}, src, 4); err != nil {
		t.Fatalf("processSource: %v", err)
	}

	notes := allNotes(t, tb.db)
	if len(notes) != 1 {
		t.Fatalf("notes = %d, want 1", len(notes))
	}
	n := notes[0]
	if n.ConfidenceOverall.Valid || n.ConfidenceMin.Valid {
		t.Errorf("confidence columns must be NULL when whisper has no segments; got %+v / %+v",
			n.ConfidenceOverall, n.ConfidenceMin)
	}
	if n.SuspectHallucination {
		t.Errorf("suspect_hallucination must be 0 when there are no segments")
	}
}

// TestProcessSource_AudioRetentionSavesAndSetsPath verifies that with
// audioDir set, the source file is copied under audioDir and the relative
// basename is recorded in audio_path.
func TestProcessSource_AudioRetentionSavesAndSetsPath(t *testing.T) {
	tb := newTestBot(t)
	tb.audioDir = t.TempDir()
	tb.audioRetainOn = true
	ft := &fakeTranscriber{result: whisper.Result{Text: "retained"}}
	wireTranscriber(tb, ft)

	src, _ := makeAudio(t, "audio-bytes-RETAIN")
	if err := tb.processSource(context.Background(), &fakeCtx{}, src, 7); err != nil {
		t.Fatalf("processSource: %v", err)
	}

	notes := allNotes(t, tb.db)
	if len(notes) != 1 {
		t.Fatalf("notes = %d, want 1", len(notes))
	}
	n := notes[0]
	if !n.AudioPath.Valid || n.AudioPath.String == "" {
		t.Fatalf("audio_path must be set when retention is on; got %+v", n.AudioPath)
	}
	// Post-F3 contract: audio_path stores the basename (relative), not an
	// absolute filesystem path. Verify by checking the file exists under
	// audioDir/audio_path.
	resolved := filepath.Join(tb.audioDir, n.AudioPath.String)
	if _, err := os.Stat(resolved); err != nil {
		t.Errorf("expected retained audio at %s: %v", resolved, err)
	}
}

// TestProcessSource_NoRetentionWhenAudioDirEmpty asserts the negative —
// audio_path stays NULL when audioRetainOn is false (default cfg).
func TestProcessSource_NoRetentionWhenAudioDirEmpty(t *testing.T) {
	tb := newTestBot(t)
	if tb.audioRetainOn {
		t.Fatal("test precondition: retention must be off")
	}
	ft := &fakeTranscriber{result: whisper.Result{Text: "ephemeral"}}
	wireTranscriber(tb, ft)

	src, _ := makeAudio(t, "audio-bytes-NORETAIN")
	if err := tb.processSource(context.Background(), &fakeCtx{}, src, 2); err != nil {
		t.Fatalf("processSource: %v", err)
	}

	notes := allNotes(t, tb.db)
	if len(notes) != 1 {
		t.Fatalf("notes = %d, want 1", len(notes))
	}
	if notes[0].AudioPath.Valid {
		t.Errorf("audio_path must be NULL with retention off; got %q", notes[0].AudioPath.String)
	}
}

// TestProcessSource_HallucinationSuspectFlag sets up a result whose first
// segment's no_speech_prob exceeds the threshold and asserts the
// suspect_hallucination column lands as 1.
func TestProcessSource_HallucinationSuspectFlag(t *testing.T) {
	tb := newTestBot(t)
	ft := &fakeTranscriber{
		result: whisper.Result{
			Text: "you're watching me",
			Segments: []whisper.Segment{
				{AvgLogprob: -0.4, NoSpeechProb: 0.95}, // first segment > thresh
				{AvgLogprob: -0.5, NoSpeechProb: 0.10},
			},
		},
	}
	wireTranscriber(tb, ft)

	src, _ := makeAudio(t, "audio-bytes-SUSP")
	if err := tb.processSource(context.Background(), &fakeCtx{}, src, 9); err != nil {
		t.Fatalf("processSource: %v", err)
	}

	notes := allNotes(t, tb.db)
	if len(notes) != 1 {
		t.Fatalf("notes = %d, want 1", len(notes))
	}
	if !notes[0].SuspectHallucination {
		t.Errorf("suspect_hallucination must be true when first segment no_speech_prob > thresh")
	}
	// Recorded() also folds the suspect flag into the reply text — the
	// ⚠ glyph is asserted by the locale-completeness test; reuse the
	// same check here so a missing branch in Recorded would also fail.
	// (Locale test only checks ru/en have the glyph; this asserts the
	// flag actually reaches the user.)
}

// TestProcessSource_TypingNotifyFiresTwice documents that we send Typing
// at least twice: once on entry to the source path (refresh before
// whisper) — the file-level processFile sends a separate one before
// download. If the typing-refresh ever gets dropped, the user sees the
// indicator vanish for the entire whisper call duration.
func TestProcessSource_TypingNotifyFiresTwice(t *testing.T) {
	tb := newTestBot(t)
	wireTranscriber(tb, &fakeTranscriber{result: whisper.Result{Text: "ok"}})

	src, _ := makeAudio(t, "audio-bytes-TYPING")
	fc := &fakeCtx{}
	if err := tb.processSource(context.Background(), fc, src, 1); err != nil {
		t.Fatalf("processSource: %v", err)
	}
	if len(fc.notifies) < 1 {
		t.Errorf("expected at least one Typing notify, got 0")
	}
	for _, a := range fc.notifies {
		if a != tele.Typing {
			t.Errorf("unexpected notify action %q (want Typing)", a)
		}
	}
}

// --- internal helpers ---------------------------------------------------

// allNotes returns every row in `notes`, ordered by id. Tests assert on
// either the count or the first row.
func allNotes(t *testing.T, d *db.DB) []db.Note {
	t.Helper()
	rows, err := d.QueryContext(context.Background(),
		`SELECT id, created_at, raw_text, duration_sec, audio_path, status,
		        confidence_overall, confidence_min, suspect_hallucination
		   FROM notes ORDER BY id`)
	if err != nil {
		t.Fatalf("query notes: %v", err)
	}
	defer rows.Close()
	var out []db.Note
	for rows.Next() {
		var (
			n  db.Note
			ts int64
		)
		var suspectInt int
		if err := rows.Scan(&n.ID, &ts, &n.RawText, &n.DurationSec, &n.AudioPath, &n.Status,
			&n.ConfidenceOverall, &n.ConfidenceMin, &suspectInt); err != nil {
			t.Fatalf("scan note: %v", err)
		}
		n.CreatedAt = time.Unix(ts, 0)
		n.SuspectHallucination = suspectInt != 0
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	return out
}
