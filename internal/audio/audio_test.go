package audio

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"voicelog/internal/db"
	"voicelog/internal/db/migrations"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSaveOriginal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeFile(t, src, "OggS......opus payload")

	dest := filepath.Join(dir, "audio")
	got, err := SaveOriginal(src, dest, 42)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	// Post-F3: SaveOriginal returns the RELATIVE path (basename only).
	// The absolute on-disk location is dest/<id>.oga, resolved by Resolve
	// at read time.
	const wantRel = "42.oga"
	if got != wantRel {
		t.Errorf("returned rel path: want %q, got %q", wantRel, got)
	}
	onDisk := filepath.Join(dest, got)
	f, err := os.Open(onDisk)
	if err != nil {
		t.Fatalf("open dest: %v", err)
	}
	defer f.Close()
	body, _ := io.ReadAll(f)
	if string(body) != "OggS......opus payload" {
		t.Errorf("payload mismatch: %q", body)
	}
	// Perms must be 0600 owner-only — only meaningful on Unix; Windows
	// reports a constant 0666 for files and ignores the mode argument.
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(onDisk)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("perm: want 0600, got %#o", info.Mode().Perm())
		}
	}
}

func TestResolve(t *testing.T) {
	dir := filepath.Join(string(os.PathSeparator)+"data", "audio")
	cases := []struct {
		name, audioDir, stored, want string
	}{
		{"empty stored", dir, "", ""},
		{"relative basename", dir, "42.oga", filepath.Join(dir, "42.oga")},
		{"relative subpath", dir, filepath.Join("subdir", "9.oga"), filepath.Join(dir, "subdir", "9.oga")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Resolve(c.audioDir, c.stored)
			if got != c.want {
				t.Errorf("Resolve(%q, %q) = %q, want %q", c.audioDir, c.stored, got, c.want)
			}
		})
	}
	// Absolute legacy paths must round-trip unchanged regardless of
	// audioDir. Use t.TempDir() so the path is platform-native absolute.
	legacy := filepath.Join(t.TempDir(), "legacy.oga")
	if got := Resolve(dir, legacy); got != legacy {
		t.Errorf("Resolve abs legacy: want unchanged %q, got %q", legacy, got)
	}
}

func TestSaveOriginal_MissingSrc(t *testing.T) {
	dir := t.TempDir()
	_, err := SaveOriginal(filepath.Join(dir, "does-not-exist"), filepath.Join(dir, "audio"), 1)
	if err == nil {
		t.Fatal("want error for missing source")
	}
}

func TestPathInside(t *testing.T) {
	// Use platform-native absolute paths so the test passes on Linux,
	// macOS, AND Windows (where `/data/...` would resolve differently).
	base := t.TempDir()
	outsideBase := t.TempDir()
	cases := []struct {
		dir, p string
		want   bool
	}{
		{base, filepath.Join(base, "42.oga"), true},
		{base, filepath.Join(base, "sub", "42.oga"), true},
		{base, base, false},                                 // dir itself, not inside
		{base, base + string(os.PathSeparator), false},      // same w/ trailing sep
		{base, filepath.Join(outsideBase, "evil.oga"), false}, // sibling temp dir
		{base, filepath.Join(base, "..", "outside"), false}, // traversal upward
	}
	for _, c := range cases {
		got := pathInside(c.dir, c.p)
		if got != c.want {
			t.Errorf("pathInside(%q, %q) = %v, want %v", c.dir, c.p, got, c.want)
		}
	}
}

// TestJanitor_DeletesOldOnly exercises the legacy absolute-path code
// path. Pre-F3 notes have absolute audio_path values; the janitor must
// still process them when they fall under the current managed dir.
func TestJanitor_DeletesOldOnly(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	dir := t.TempDir()

	// Two retained notes: one fresh (1 hour ago), one old (3 days ago).
	now := time.Now()
	oldNote := insertWithTimestamp(t, d, now.Add(-72*time.Hour), "old text")
	freshNote := insertWithTimestamp(t, d, now.Add(-1*time.Hour), "fresh text")

	oldPath := filepath.Join(dir, "old.oga")
	freshPath := filepath.Join(dir, "fresh.oga")
	writeFile(t, oldPath, "old payload")
	writeFile(t, freshPath, "fresh payload")
	if err := d.SetAudioPath(ctx, oldNote, oldPath); err != nil {
		t.Fatalf("set old path: %v", err)
	}
	if err := d.SetAudioPath(ctx, freshNote, freshPath); err != nil {
		t.Fatalf("set fresh path: %v", err)
	}

	dirAbs, _ := filepath.Abs(dir)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	runOnce(ctx, d, dirAbs, 2, logger) // retention = 2 days

	// Old file gone; fresh file kept.
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("old file should be deleted; stat err=%v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Errorf("fresh file should remain: %v", err)
	}

	// Old note's audio_path is nulled; fresh note's preserved.
	gotOld, _ := d.GetNote(ctx, oldNote)
	if gotOld.AudioPath.Valid {
		t.Errorf("old note audio_path should be NULL, got %q", gotOld.AudioPath.String)
	}
	gotFresh, _ := d.GetNote(ctx, freshNote)
	if !gotFresh.AudioPath.Valid || gotFresh.AudioPath.String != freshPath {
		t.Errorf("fresh note audio_path lost: %+v", gotFresh.AudioPath)
	}
}

// TestJanitor_RelativePath_NewFormat is the post-F3 happy path: stored
// audio_path is a basename, janitor joins it under dirAbs at read time
// and successfully removes the file.
func TestJanitor_RelativePath_NewFormat(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	dirAbs, _ := filepath.Abs(dir)

	now := time.Now()
	id := insertWithTimestamp(t, d, now.Add(-72*time.Hour), "old text")

	// SaveOriginal's contract: file at dir/<id>.oga, return basename.
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "src.oga")
	writeFile(t, src, "payload")
	rel, err := SaveOriginal(src, dir, id)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if filepath.IsAbs(rel) {
		t.Fatalf("SaveOriginal must return relative path, got %q", rel)
	}
	if err := d.SetAudioPath(ctx, id, rel); err != nil {
		t.Fatalf("set path: %v", err)
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	runOnce(ctx, d, dirAbs, 2, logger)

	onDisk := filepath.Join(dirAbs, rel)
	if _, err := os.Stat(onDisk); !os.IsNotExist(err) {
		t.Errorf("file should be deleted at resolved path %q; stat err=%v", onDisk, err)
	}
	got, _ := d.GetNote(ctx, id)
	if got.AudioPath.Valid {
		t.Errorf("audio_path should be nulled; got %q", got.AudioPath.String)
	}
}

// TestJanitor_AudioDirChange_RelativeFollowsDir is the F3 fix in
// action. A note is written with one audioDir, then read by the janitor
// running against a DIFFERENT audioDir. With relative paths, the file
// in the NEW dir is resolved correctly. (Compare to legacy absolute
// paths, which would leak — exercised by TestJanitor_RefusesPathOutsideDir.)
func TestJanitor_AudioDirChange_RelativeFollowsDir(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	oldDir := t.TempDir()
	newDir := t.TempDir()

	now := time.Now()
	id := insertWithTimestamp(t, d, now.Add(-72*time.Hour), "old text")

	// Write in the OLD dir, set DB to the relative basename only.
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "src.oga")
	writeFile(t, src, "payload")
	rel, err := SaveOriginal(src, oldDir, id)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := d.SetAudioPath(ctx, id, rel); err != nil {
		t.Fatalf("set path: %v", err)
	}

	// Operator changes AUDIO_DIR. Janitor runs against newDir. The OLD
	// file (in oldDir) is now unreachable through audio_path — that's
	// expected. But we ALSO place a file at newDir/<rel> to confirm the
	// janitor resolves to the active dir and removes from THERE.
	newOnDisk := filepath.Join(newDir, rel)
	writeFile(t, newOnDisk, "payload in new dir")

	newDirAbs, _ := filepath.Abs(newDir)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	runOnce(ctx, d, newDirAbs, 2, logger)

	if _, err := os.Stat(newOnDisk); !os.IsNotExist(err) {
		t.Errorf("file in new dir should be deleted; stat err=%v", err)
	}
	got, _ := d.GetNote(ctx, id)
	if got.AudioPath.Valid {
		t.Errorf("audio_path should be nulled; got %q", got.AudioPath.String)
	}
}

func TestJanitor_RefusesPathOutsideDir(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	managedDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "evil.oga")
	writeFile(t, outsideFile, "should not be touched")

	id := insertWithTimestamp(t, d, time.Now().Add(-72*time.Hour), "old")
	if err := d.SetAudioPath(ctx, id, outsideFile); err != nil {
		t.Fatalf("set path: %v", err)
	}

	dirAbs, _ := filepath.Abs(managedDir)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	runOnce(ctx, d, dirAbs, 2, logger)

	// File must NOT be touched.
	if _, err := os.Stat(outsideFile); err != nil {
		t.Errorf("outside file should be untouched: %v", err)
	}
	// DB pointer is cleared (defensive — don't reference a non-managed path).
	got, _ := d.GetNote(ctx, id)
	if got.AudioPath.Valid {
		t.Errorf("audio_path should be nulled for unmanaged path; got %q", got.AudioPath.String)
	}
}

func TestRelativizeLegacyPaths(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	dirAbs, _ := filepath.Abs(dir)
	otherDir := t.TempDir()
	otherAbs, _ := filepath.Abs(otherDir)

	// Seed four scenarios:
	//   1. row with NULL audio_path        — must be ignored
	//   2. row with relative path          — already in new format, skip
	//   3. row with abs inside current dir — rewrite to basename
	//   4. row with abs OUTSIDE current dir — leave alone
	null := insertWithTimestamp(t, d, time.Now(), "no audio")
	_ = null

	relRow := insertWithTimestamp(t, d, time.Now(), "already relative")
	if err := d.SetAudioPath(ctx, relRow, "42.oga"); err != nil {
		t.Fatalf("set relRow: %v", err)
	}

	insideRow := insertWithTimestamp(t, d, time.Now(), "abs inside")
	insidePath := filepath.Join(dirAbs, "100.oga")
	if err := d.SetAudioPath(ctx, insideRow, insidePath); err != nil {
		t.Fatalf("set insideRow: %v", err)
	}

	outsideRow := insertWithTimestamp(t, d, time.Now(), "abs outside")
	outsidePath := filepath.Join(otherAbs, "200.oga")
	if err := d.SetAudioPath(ctx, outsideRow, outsidePath); err != nil {
		t.Fatalf("set outsideRow: %v", err)
	}

	n, err := RelativizeLegacyPaths(ctx, d, dirAbs)
	if err != nil {
		t.Fatalf("relativize: %v", err)
	}
	if n != 1 {
		t.Errorf("rewritten count: want 1 (insideRow only), got %d", n)
	}

	// Verify final state per row.
	if got, _ := d.GetNote(ctx, relRow); got.AudioPath.String != "42.oga" {
		t.Errorf("relRow audio_path: want %q (unchanged), got %q", "42.oga", got.AudioPath.String)
	}
	if got, _ := d.GetNote(ctx, insideRow); got.AudioPath.String != "100.oga" {
		t.Errorf("insideRow audio_path: want %q (normalized to basename), got %q", "100.oga", got.AudioPath.String)
	}
	if got, _ := d.GetNote(ctx, outsideRow); got.AudioPath.String != outsidePath {
		t.Errorf("outsideRow audio_path: want %q (unchanged), got %q", outsidePath, got.AudioPath.String)
	}

	// Idempotency: a second pass should normalize zero rows.
	n2, err := RelativizeLegacyPaths(ctx, d, dirAbs)
	if err != nil {
		t.Fatalf("relativize 2nd pass: %v", err)
	}
	if n2 != 0 {
		t.Errorf("idempotency violated: second pass rewrote %d rows", n2)
	}
}

func TestRelativizeLegacyPaths_EmptyDir(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	// audioDir == "" means retention disabled — bail without touching DB.
	n, err := RelativizeLegacyPaths(ctx, d, "")
	if err != nil {
		t.Fatalf("err for empty dir: %v", err)
	}
	if n != 0 {
		t.Errorf("rewritten: want 0 for empty audioDir, got %d", n)
	}
}

func TestScanOrphans(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	dirAbs, _ := filepath.Abs(dir)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	// Seed three files on disk: two known (relative + abs-inside) and
	// two orphans. Subdir contents should be ignored.
	writeFile(t, filepath.Join(dirAbs, "1.oga"), "known relative")
	writeFile(t, filepath.Join(dirAbs, "2.oga"), "known absolute")
	writeFile(t, filepath.Join(dirAbs, "orphan-a.oga"), "no DB row")
	writeFile(t, filepath.Join(dirAbs, "orphan-b.oga"), "also no DB row")
	writeFile(t, filepath.Join(dirAbs, "notes.txt"), "non-oga ignored")
	if err := os.MkdirAll(filepath.Join(dirAbs, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dirAbs, "sub", "nested.oga"), "subdir ignored")

	id1 := insertWithTimestamp(t, d, time.Now(), "known relative")
	if err := d.SetAudioPath(ctx, id1, "1.oga"); err != nil {
		t.Fatal(err)
	}
	id2 := insertWithTimestamp(t, d, time.Now(), "known absolute")
	if err := d.SetAudioPath(ctx, id2, filepath.Join(dirAbs, "2.oga")); err != nil {
		t.Fatal(err)
	}

	n, err := ScanOrphans(ctx, d, dirAbs, logger)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n != 2 {
		t.Errorf("orphan count: want 2 (orphan-a + orphan-b), got %d", n)
	}
}

// TestJanitor_ReturnsImmediatelyWhenDisabled verifies the early-exit
// branches: retentionDays <= 0 OR dir == "" → return without starting
// the ticker. Caller passes a cancellable ctx; we don't cancel it
// because the function must finish on its own.
func TestJanitor_ReturnsImmediatelyWhenDisabled(t *testing.T) {
	d := openTestDB(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	cases := []struct {
		name          string
		dir           string
		retentionDays int
	}{
		{"retention_zero", t.TempDir(), 0},
		{"retention_negative", t.TempDir(), -1},
		{"empty_dir", "", 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				Janitor(context.Background(), d, c.dir, c.retentionDays, logger)
				close(done)
			}()
			select {
			case <-done:
				// success — returned without needing ctx cancel
			case <-time.After(2 * time.Second):
				t.Fatalf("Janitor did not return for disabled config (%s)", c.name)
			}
		})
	}
}

// TestJanitor_ExitsOnContextCancel runs the janitor for real and
// asserts it shuts down promptly when the parent ctx is cancelled.
// JanitorPeriod is 6h so the ticker never fires during the test —
// the cancel must unblock the select on ctx.Done() directly.
func TestJanitor_ExitsOnContextCancel(t *testing.T) {
	d := openTestDB(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		Janitor(ctx, d, t.TempDir(), 7, logger)
		close(done)
	}()

	// Give the goroutine a moment to enter its select.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// shut down cleanly
	case <-time.After(2 * time.Second):
		t.Fatalf("Janitor did not exit within 2s of ctx cancel")
	}
}

func TestScanOrphans_MissingDir(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	// Pass a path that doesn't exist — should be treated as "0 orphans"
	// not an error, so first-run before any file lands is fine.
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	n, err := ScanOrphans(ctx, d, missing, logger)
	if err != nil {
		t.Fatalf("expected nil err for missing dir, got %v", err)
	}
	if n != 0 {
		t.Errorf("orphans: want 0 for missing dir, got %d", n)
	}
}

// insertWithTimestamp inserts a note with a controlled created_at so the
// janitor's cutoff logic can be exercised deterministically.
func insertWithTimestamp(t *testing.T, d *db.DB, when time.Time, text string) int64 {
	t.Helper()
	res, err := d.ExecContext(context.Background(),
		`INSERT INTO notes (created_at, raw_text, duration_sec) VALUES (?, ?, ?)`,
		when.Unix(), text, 5)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}
