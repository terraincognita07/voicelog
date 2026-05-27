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
	"voicelog/migrations"
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
	want := filepath.Join(dest, "42.oga")
	if got != want {
		t.Errorf("returned path: want %q, got %q", want, got)
	}
	// File contents must match.
	f, err := os.Open(got)
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
		info, _ := os.Stat(got)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("perm: want 0600, got %#o", info.Mode().Perm())
		}
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
