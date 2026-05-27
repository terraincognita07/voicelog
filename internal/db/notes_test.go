package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"voicelog/internal/db"
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
