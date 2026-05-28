package db_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"voicelog/internal/db"
	"voicelog/internal/db/migrations"
)

func TestInsertAndList(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id1, err := d.InsertNote(ctx, "первая заметка", 3)
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	id2, err := d.InsertNote(ctx, "вторая заметка", 7)
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	n, err := d.CountPending(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 pending, got %d", n)
	}

	pending, err := d.ListPending(ctx, 50)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("want 2 rows, got %d", len(pending))
	}
	// Newest first.
	if pending[0].ID != id2 || pending[1].ID != id1 {
		t.Fatalf("wrong order: got %d,%d", pending[0].ID, pending[1].ID)
	}
	if pending[0].Status != db.StatusPending {
		t.Fatalf("want status pending, got %q", pending[0].Status)
	}
	if pending[0].DurationSec.Int64 != 7 {
		t.Fatalf("want duration 7, got %d", pending[0].DurationSec.Int64)
	}
}

func TestGetNotesInRange(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	// Three notes spread across time. Use raw SQL to control created_at.
	for i, ts := range []int64{1700_000_000, 1700_000_100, 1700_000_200} {
		_, err := d.ExecContext(ctx,
			`INSERT INTO notes (created_at, raw_text) VALUES (?, ?)`,
			ts, "note "+string(rune('A'+i)))
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	from := time.Unix(1700_000_050, 0)
	to := time.Unix(1700_000_250, 0)
	notes, err := d.GetNotesInRange(ctx, from, to, "", 0, true)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("want 2 notes in range, got %d", len(notes))
	}
	if notes[0].CreatedAt.Unix() != 1700_000_200 || notes[1].CreatedAt.Unix() != 1700_000_100 {
		t.Fatalf("unexpected order: %v", notes)
	}

	// Status filter.
	_, _ = d.ExecContext(ctx, `UPDATE notes SET status='analyzed' WHERE created_at = 1700000200`)
	pending, err := d.GetNotesInRange(ctx, from, to, "pending", 0, false)
	if err != nil {
		t.Fatalf("range pending: %v", err)
	}
	if len(pending) != 1 || pending[0].CreatedAt.Unix() != 1700_000_100 {
		t.Fatalf("status filter failed: %+v", pending)
	}
}

func TestGetNotesInRangeRespectsCap(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	// Insert MaxNotesInRange+10 notes, all in the same minute.
	base := int64(1_700_000_000)
	for i := 0; i < db.MaxNotesInRange+10; i++ {
		_, err := d.ExecContext(ctx,
			`INSERT INTO notes (created_at, raw_text) VALUES (?, ?)`, base+int64(i), "x")
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	from := time.Unix(base-1, 0)
	to := time.Unix(base+int64(db.MaxNotesInRange)+100, 0)

	// limit=0 → clamp to MaxNotesInRange.
	notes, err := d.GetNotesInRange(ctx, from, to, "", 0, true)
	if err != nil {
		t.Fatalf("range default: %v", err)
	}
	if len(notes) != db.MaxNotesInRange {
		t.Fatalf("default limit: want %d, got %d", db.MaxNotesInRange, len(notes))
	}

	// Over-cap value also clamps.
	notes, err = d.GetNotesInRange(ctx, from, to, "", db.MaxNotesInRange*2, true)
	if err != nil {
		t.Fatalf("range over-cap: %v", err)
	}
	if len(notes) != db.MaxNotesInRange {
		t.Fatalf("over-cap: want %d, got %d", db.MaxNotesInRange, len(notes))
	}

	// Smaller explicit limit honored.
	notes, err = d.GetNotesInRange(ctx, from, to, "", 7, true)
	if err != nil {
		t.Fatalf("range small limit: %v", err)
	}
	if len(notes) != 7 {
		t.Fatalf("small limit: want 7, got %d", len(notes))
	}
}

func TestSearchNotes(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	if _, err := d.InsertNote(ctx, "купить молоко завтра", 2); err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	if _, err := d.InsertNote(ctx, "идея для проекта voicelog с MCP", 5); err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	if _, err := d.InsertNote(ctx, "позвонить маме сегодня", 3); err != nil {
		t.Fatalf("seed 3: %v", err)
	}

	hits, err := d.SearchNotes(ctx, "voicelog", 10, false)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(hits))
	}
	if !strings.Contains(hits[0].RawText, "voicelog") {
		t.Fatalf("wrong row matched: %+v", hits[0])
	}
	if hits[0].Rank == 0 {
		t.Fatalf("rank should be non-zero (bm25 returns negative-ish floats)")
	}
	if !strings.Contains(hits[0].Snippet, "<<voicelog>>") {
		t.Fatalf("snippet should wrap matched term in << >>, got %q", hits[0].Snippet)
	}

	_, err = d.SearchNotes(ctx, "", 10, false)
	if err == nil {
		t.Fatalf("expected error on empty query")
	}
}

func TestSearchNotesExcludesDiscardedByDefault(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, _ := d.InsertNote(ctx, "уникальное слово фламинго один", 1)
	if err := d.MarkDiscarded(ctx, id); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := d.InsertNote(ctx, "уникальное слово фламинго два", 1); err != nil {
		t.Fatalf("insert pending: %v", err)
	}

	hits, err := d.SearchNotes(ctx, "фламинго", 10, false)
	if err != nil {
		t.Fatalf("search default: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("default must hide discarded: want 1 hit, got %d", len(hits))
	}
	if hits[0].Status != db.StatusPending {
		t.Fatalf("expected pending hit, got %q", hits[0].Status)
	}

	all, err := d.SearchNotes(ctx, "фламинго", 10, true)
	if err != nil {
		t.Fatalf("search include: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("include_discarded must surface both: got %d", len(all))
	}
}

func TestGetNotesInRangeExcludesDiscardedByDefault(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	now := time.Now()
	if _, err := d.InsertNote(ctx, "pending one", 1); err != nil {
		t.Fatalf("insert: %v", err)
	}
	id2, _ := d.InsertNote(ctx, "to discard", 1)
	if err := d.MarkDiscarded(ctx, id2); err != nil {
		t.Fatalf("discard: %v", err)
	}
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)

	def, err := d.GetNotesInRange(ctx, from, to, "", 0, false)
	if err != nil {
		t.Fatalf("range default: %v", err)
	}
	if len(def) != 1 {
		t.Fatalf("default must exclude discarded: got %d", len(def))
	}

	all, err := d.GetNotesInRange(ctx, from, to, "", 0, true)
	if err != nil {
		t.Fatalf("range include: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("include_discarded must show both: got %d", len(all))
	}

	// Explicit status='discarded' wins over include_discarded=false.
	only, err := d.GetNotesInRange(ctx, from, to, "discarded", 0, false)
	if err != nil {
		t.Fatalf("range status discarded: %v", err)
	}
	if len(only) != 1 || only[0].Status != db.StatusDiscarded {
		t.Fatalf("explicit status filter must surface discarded: %+v", only)
	}
}

func TestMarkAnalyzed(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id1, _ := d.InsertNote(ctx, "a", 1)
	id2, _ := d.InsertNote(ctx, "b", 1)
	id3, _ := d.InsertNote(ctx, "c", 1)
	// Discard one — must not be re-flipped.
	if err := d.MarkDiscarded(ctx, id3); err != nil {
		t.Fatalf("discard: %v", err)
	}

	n, err := d.MarkAnalyzed(ctx, []int64{id1, id2, id3, 9999})
	if err != nil {
		t.Fatalf("mark analyzed: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 updates (id1, id2 — id3 discarded, 9999 missing), got %d", n)
	}

	// Idempotent: re-running yields more updates only if rows actually change.
	// Here id1 and id2 are already 'analyzed', so MarkAnalyzed will see 0 changes
	// because SQLite's RowsAffected on UPDATE counts matched-and-changed rows only
	// when implementation differs; modernc/sqlite counts matched rows. Either way,
	// rerunning must not error.
	if _, err := d.MarkAnalyzed(ctx, []int64{id1, id2}); err != nil {
		t.Fatalf("re-mark: %v", err)
	}

	// Empty slice → 0, no error.
	n, err = d.MarkAnalyzed(ctx, nil)
	if err != nil || n != 0 {
		t.Fatalf("empty ids: n=%d err=%v", n, err)
	}
}

func TestGetNote(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.InsertNote(ctx, "hello world", 4)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	n, err := d.GetNote(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if n.ID != id || n.RawText != "hello world" || n.DurationSec.Int64 != 4 {
		t.Fatalf("unexpected note: %+v", n)
	}
	if n.Status != db.StatusPending {
		t.Fatalf("want pending, got %q", n.Status)
	}

	if _, err := d.GetNote(ctx, 99999); !errors.Is(err, db.ErrNoteNotFound) {
		t.Fatalf("want ErrNoteNotFound, got %v", err)
	}
}

func TestDiscardNotes(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id1, _ := d.InsertNote(ctx, "a", 1)
	id2, _ := d.InsertNote(ctx, "b", 1)
	id3, _ := d.InsertNote(ctx, "c", 1)
	// id3 already discarded.
	if err := d.MarkDiscarded(ctx, id3); err != nil {
		t.Fatalf("seed discard: %v", err)
	}

	n, err := d.DiscardNotes(ctx, []int64{id1, id2, id3, 99999})
	if err != nil {
		t.Fatalf("discard notes: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 flipped (id3 already discarded, 99999 missing), got %d", n)
	}

	if pending, _ := d.CountPending(ctx); pending != 0 {
		t.Fatalf("expected 0 pending after mass discard, got %d", pending)
	}

	// Empty ids → no-op.
	if n, err := d.DiscardNotes(ctx, nil); err != nil || n != 0 {
		t.Fatalf("empty ids: n=%d err=%v", n, err)
	}
}

func TestRestoreNote(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, _ := d.InsertNote(ctx, "to restore", 1)
	if err := d.MarkDiscarded(ctx, id); err != nil {
		t.Fatalf("discard: %v", err)
	}

	ok, err := d.RestoreNote(ctx, id)
	if err != nil || !ok {
		t.Fatalf("restore: ok=%v err=%v", ok, err)
	}
	got, _ := d.GetNote(ctx, id)
	if got.Status != db.StatusPending {
		t.Fatalf("want pending after restore, got %q", got.Status)
	}

	// Restoring a non-discarded note → (false, nil).
	ok, err = d.RestoreNote(ctx, id)
	if err != nil || ok {
		t.Fatalf("restore non-discarded: ok=%v err=%v", ok, err)
	}

	// Unknown id → ErrNoteNotFound.
	_, err = d.RestoreNote(ctx, 99999)
	if !errors.Is(err, db.ErrNoteNotFound) {
		t.Fatalf("want ErrNoteNotFound, got %v", err)
	}
}

func TestInsertNoteWithMeta(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	// Without meta → all confidence/suspect columns are NULL/0.
	id1, err := d.InsertNote(ctx, "no meta", 5)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	got1, _ := d.GetNote(ctx, id1)
	if got1.ConfidenceOverall.Valid || got1.ConfidenceMin.Valid {
		t.Errorf("confidence should be NULL for note without meta: %+v", got1)
	}
	if got1.SuspectHallucination {
		t.Errorf("suspect should default to false")
	}

	// With meta → values round-trip.
	overall, worst := -0.42, -0.91
	id2, err := d.InsertNoteWithMeta(ctx, "with meta", 7, db.NoteMeta{
		ConfidenceOverall:    &overall,
		ConfidenceMin:        &worst,
		SuspectHallucination: true,
	})
	if err != nil {
		t.Fatalf("insert w/ meta: %v", err)
	}
	got2, _ := d.GetNote(ctx, id2)
	if !got2.ConfidenceOverall.Valid || got2.ConfidenceOverall.Float64 != overall {
		t.Errorf("confidence_overall round-trip failed: %+v", got2.ConfidenceOverall)
	}
	if !got2.ConfidenceMin.Valid || got2.ConfidenceMin.Float64 != worst {
		t.Errorf("confidence_min round-trip failed: %+v", got2.ConfidenceMin)
	}
	if !got2.SuspectHallucination {
		t.Errorf("suspect should be true")
	}
}

func TestArchiveAndUpdateText(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.InsertNote(ctx, "original transcription", 5)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// First retranscribe: new text, no meta. Old text returned for diffing.
	oldText, err := d.ArchiveAndUpdateText(ctx, id, "first re-run text", "small-q5_1", db.NoteMeta{})
	if err != nil {
		t.Fatalf("archive 1: %v", err)
	}
	if oldText != "original transcription" {
		t.Errorf("returned old text: want original, got %q", oldText)
	}
	got, _ := d.GetNote(ctx, id)
	if got.RawText != "first re-run text" {
		t.Errorf("note not updated: %q", got.RawText)
	}

	// Second retranscribe: with confidence meta. Each run archives the
	// previous text → 2 rows in notes_history.
	overall, worst := -0.3, -0.6
	oldText2, err := d.ArchiveAndUpdateText(ctx, id, "second re-run text", "medium-q5_0", db.NoteMeta{
		ConfidenceOverall:    &overall,
		ConfidenceMin:        &worst,
		SuspectHallucination: false,
	})
	if err != nil {
		t.Fatalf("archive 2: %v", err)
	}
	if oldText2 != "first re-run text" {
		t.Errorf("returned old text 2: want first re-run, got %q", oldText2)
	}
	got2, _ := d.GetNote(ctx, id)
	if got2.RawText != "second re-run text" || !got2.ConfidenceOverall.Valid {
		t.Errorf("note state after 2nd: %+v", got2)
	}

	// notes_history should have exactly 2 rows for this note, oldest first.
	rows, err := d.QueryContext(ctx,
		`SELECT raw_text, model FROM notes_history WHERE note_id = ? ORDER BY replaced_at ASC, id ASC`, id)
	if err != nil {
		t.Fatalf("history query: %v", err)
	}
	defer rows.Close()
	var history []struct {
		text, model string
	}
	for rows.Next() {
		var h struct{ text, model string }
		var modelNull sql.NullString
		if err := rows.Scan(&h.text, &modelNull); err != nil {
			t.Fatalf("scan: %v", err)
		}
		h.model = modelNull.String
		history = append(history, h)
	}
	if len(history) != 2 {
		t.Fatalf("history rows: want 2, got %d", len(history))
	}
	if history[0].text != "original transcription" || history[0].model != "small-q5_1" {
		t.Errorf("history[0]: %+v", history[0])
	}
	if history[1].text != "first re-run text" || history[1].model != "medium-q5_0" {
		t.Errorf("history[1]: %+v", history[1])
	}
}

func TestArchiveAndUpdateText_NoteMissing(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	_, err := d.ArchiveAndUpdateText(ctx, 9999, "x", "", db.NoteMeta{})
	if !errors.Is(err, db.ErrNoteNotFound) {
		t.Fatalf("want ErrNoteNotFound, got %v", err)
	}
}

func TestArchiveAndUpdateText_FTSReflectsNewText(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, _ := d.InsertNote(ctx, "уникальное слово альфа", 3)
	if _, err := d.ArchiveAndUpdateText(ctx, id, "уникальное слово бета", "", db.NoteMeta{}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	// Existing FTS UPDATE trigger should swap old → new in the index.
	hits, _ := d.SearchNotes(ctx, "альфа", 10, false)
	if len(hits) != 0 {
		t.Errorf("old term still in FTS index after retranscribe: %d hits", len(hits))
	}
	hits, _ = d.SearchNotes(ctx, "бета", 10, false)
	if len(hits) != 1 {
		t.Errorf("new term not indexed: %d hits", len(hits))
	}
}

func TestInsertNoteWithMeta_AudioHash(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.InsertNoteWithMeta(ctx, "hashed", 3, db.NoteMeta{AudioHash: "deadbeef"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	dup, err := d.FindRecentByHash(ctx, "deadbeef", time.Minute)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if dup.ID != id {
		t.Errorf("dup id: want %d, got %d", id, dup.ID)
	}

	// Different hash → not found.
	if _, err := d.FindRecentByHash(ctx, "feedface", time.Minute); !errors.Is(err, db.ErrNoteNotFound) {
		t.Errorf("want ErrNoteNotFound for missing hash, got %v", err)
	}

	// Empty hash → not found (defensive — never match the "no hash" notes).
	if _, err := d.FindRecentByHash(ctx, "", time.Minute); !errors.Is(err, db.ErrNoteNotFound) {
		t.Errorf("want ErrNoteNotFound for empty hash, got %v", err)
	}
}

func TestFindRecentByHash_WindowExpiry(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	// Insert with a manually backdated created_at.
	res, err := d.ExecContext(ctx,
		`INSERT INTO notes (created_at, raw_text, duration_sec, audio_hash) VALUES (?, ?, ?, ?)`,
		time.Now().Add(-10*time.Minute).Unix(), "old", 3, "abc")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id, _ := res.LastInsertId()

	// 5-minute window → out of range.
	if _, err := d.FindRecentByHash(ctx, "abc", 5*time.Minute); !errors.Is(err, db.ErrNoteNotFound) {
		t.Errorf("note older than window must not match: %v", err)
	}
	// 30-minute window → matches.
	dup, err := d.FindRecentByHash(ctx, "abc", 30*time.Minute)
	if err != nil {
		t.Fatalf("wider window: %v", err)
	}
	if dup.ID != id {
		t.Errorf("dup id: want %d, got %d", id, dup.ID)
	}
}

func TestHealth(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	_, _ = d.InsertNote(ctx, "one", 1)
	_, _ = d.InsertNote(ctx, "two", 1)

	rep, err := d.Health(ctx, false)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if rep.IntegrityCheck != "ok" {
		t.Errorf("integrity_check: want ok, got %q", rep.IntegrityCheck)
	}
	if rep.QuickCheck != "ok" {
		t.Errorf("quick_check: want ok, got %q", rep.QuickCheck)
	}
	if rep.NoteCount != 2 {
		t.Errorf("note_count: want 2, got %d", rep.NoteCount)
	}
	if rep.DBSizeBytes <= 0 {
		t.Errorf("db_size_bytes should be > 0, got %d", rep.DBSizeBytes)
	}
}

// TestHealthQuickOnly asserts the quickOnly=true branch: the full
// integrity_check is skipped (IntegrityCheck = IntegrityCheckSkipped),
// quick_check still runs, the rest of the report is populated.
func TestHealthQuickOnly(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	_, _ = d.InsertNote(ctx, "one", 1)

	rep, err := d.Health(ctx, true)
	if err != nil {
		t.Fatalf("health quick: %v", err)
	}
	if rep.IntegrityCheck != db.IntegrityCheckSkipped {
		t.Errorf("integrity_check: want %q, got %q", db.IntegrityCheckSkipped, rep.IntegrityCheck)
	}
	if rep.QuickCheck != "ok" {
		t.Errorf("quick_check: want ok, got %q", rep.QuickCheck)
	}
	if rep.NoteCount != 1 {
		t.Errorf("note_count: want 1, got %d", rep.NoteCount)
	}
	if rep.DBSizeBytes <= 0 {
		t.Errorf("db_size_bytes should be > 0, got %d", rep.DBSizeBytes)
	}
}

func TestMigrateIsTracked(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	// Re-applying Migrate must be a no-op (and especially must not fail
	// on ALTER ADD COLUMN in 003).
	if err := d.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := d.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("third migrate: %v", err)
	}

	// schema_migrations table must record at least the known names.
	var count int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations WHERE name IN ('001_init.sql', '002_vocab.sql', '003_confidence.sql', '004_notes_history.sql', '005_audio_hash.sql')`).
		Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 5 {
		t.Errorf("want 5 tracked migrations, got %d", count)
	}
}

func TestDiscardAllPending(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id1, _ := d.InsertNote(ctx, "a", 1)
	id2, _ := d.InsertNote(ctx, "b", 1)
	id3, _ := d.InsertNote(ctx, "c", 1)
	// Mix the statuses: id3 → analyzed must NOT flip; id1, id2 stay pending.
	if _, err := d.MarkAnalyzed(ctx, []int64{id3}); err != nil {
		t.Fatalf("mark analyzed: %v", err)
	}

	n, err := d.DiscardAllPending(ctx)
	if err != nil {
		t.Fatalf("clear pending: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 discarded (id1, id2), got %d", n)
	}

	// id3 must still be analyzed (untouched).
	got, _ := d.GetNote(ctx, id3)
	if got.Status != db.StatusAnalyzed {
		t.Fatalf("id3 must remain analyzed, got %q", got.Status)
	}

	// Re-run: no pending left.
	n, err = d.DiscardAllPending(ctx)
	if err != nil {
		t.Fatalf("clear pending (idempotent): %v", err)
	}
	if n != 0 {
		t.Fatalf("second clear must flip 0 rows, got %d", n)
	}

	// id1, id2 are now discarded.
	for _, id := range []int64{id1, id2} {
		got, _ := d.GetNote(ctx, id)
		if got.Status != db.StatusDiscarded {
			t.Fatalf("id %d should be discarded, got %q", id, got.Status)
		}
	}
}

func TestMarkDiscarded(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.InsertNote(ctx, "to discard", 1)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := d.MarkDiscarded(ctx, id); err != nil {
		t.Fatalf("discard: %v", err)
	}

	n, _ := d.CountPending(ctx)
	if n != 0 {
		t.Fatalf("expected 0 pending after discard, got %d", n)
	}

	// Idempotency: second call must report ErrNoteNotFound (already discarded).
	err = d.MarkDiscarded(ctx, id)
	if !errors.Is(err, db.ErrNoteNotFound) {
		t.Fatalf("want ErrNoteNotFound on second discard, got %v", err)
	}

	// Non-existent id.
	err = d.MarkDiscarded(ctx, 99999)
	if !errors.Is(err, db.ErrNoteNotFound) {
		t.Fatalf("want ErrNoteNotFound for missing id, got %v", err)
	}

	// Recent should still surface the discarded row.
	recent, _ := d.ListRecent(ctx, 10)
	if len(recent) != 1 || recent[0].Status != db.StatusDiscarded {
		t.Fatalf("recent should show discarded row, got %+v", recent)
	}
}

// TestDiscardNotes_CallbackFlood models the "user taps the same
// [🗑] button 50 times rapid-fire on a phone" scenario. The bot's
// callback handler dispatches one DiscardNotes call per tap;
// nothing serializes them. The behavior contract we promise:
//
//  1. No panic / no SQLITE error under the storm.
//  2. The row ends up `discarded` exactly once — not "discarded
//     twice" or in an inconsistent intermediate state.
//  3. Across N concurrent calls, the sum of `updated` returned by
//     DiscardNotes is exactly 1. The first winning UPDATE flips
//     status; every subsequent one matches "status != 'discarded'"
//     filter against the already-flipped row and reports
//     updated=0. That guarantees idempotency at the SQL level —
//     even if the bot retries a callback because the user
//     double-tapped through bad latency.
//
// MarkAnalyzed has the same contract; covered by a sibling test.
func TestDiscardNotes_CallbackFlood(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	id, err := d.InsertNote(ctx, "to be hammered", 5)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	const N = 50
	var (
		wg          sync.WaitGroup
		mu          sync.Mutex
		totalFlips  int
		firstErr    error
		startSignal = make(chan struct{})
	)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startSignal // line every goroutine up before they fire
			n, err := d.DiscardNotes(ctx, []int64{id})
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			totalFlips += n
		}()
	}
	close(startSignal)
	wg.Wait()

	if firstErr != nil {
		t.Errorf("DiscardNotes errored under flood: %v", firstErr)
	}
	if totalFlips != 1 {
		t.Errorf("idempotency: want exactly 1 flip across %d calls, got %d", N, totalFlips)
	}
	got, err := d.GetNote(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != db.StatusDiscarded {
		t.Errorf("final status: want discarded, got %q", got.Status)
	}
}

// TestMarkAnalyzed_CallbackFlood mirrors the discard-flood test for
// the analyzed transition. Same idempotency contract: among N
// concurrent MarkAnalyzed calls on the same id, exactly one
// UPDATE actually flips the row; the rest report updated=0
// because the narrow filter `status = 'pending'` no longer matches
// after the winner commits.
//
// Until 2026-05-28 MarkAnalyzed used `WHERE status != 'discarded'`,
// which counted every `analyzed → analyzed` no-op UPDATE as
// affected through modernc/sqlite's match-counting semantics, so a
// 50× flood would report 50 flips. This test caught that and the
// SQL was narrowed.
func TestMarkAnalyzed_CallbackFlood(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	id, err := d.InsertNote(ctx, "to be analyzed under flood", 5)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	const N = 50
	var (
		wg          sync.WaitGroup
		mu          sync.Mutex
		totalFlips  int
		firstErr    error
		startSignal = make(chan struct{})
	)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startSignal
			n, err := d.MarkAnalyzed(ctx, []int64{id})
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			totalFlips += n
		}()
	}
	close(startSignal)
	wg.Wait()

	if firstErr != nil {
		t.Errorf("MarkAnalyzed errored under flood: %v", firstErr)
	}
	// Narrow `status = 'pending'` filter: first call flips pending →
	// analyzed (returns 1), every subsequent call sees status =
	// analyzed and matches nothing (returns 0). Sum across the
	// flood is exactly 1.
	if totalFlips != 1 {
		t.Errorf("idempotency: want exactly 1 flip across %d calls, got %d", N, totalFlips)
	}
	got, err := d.GetNote(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != db.StatusAnalyzed {
		t.Errorf("final status: want analyzed, got %q", got.Status)
	}
}
