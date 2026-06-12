package telegram

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestSweepStaleTempDirs(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		return p
	}
	// Two orphaned capture dirs + one unrelated dir that must survive.
	mk("voicelog-abc123")
	mk("voicelog-xyz789")
	keep := mk("some-other-dir")

	n, err := SweepStaleTempDirs(dir, logger)
	if err != nil {
		t.Fatalf("SweepStaleTempDirs: %v", err)
	}
	if n != 2 {
		t.Fatalf("removed = %d, want 2", n)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "voicelog-*")); len(matches) != 0 {
		t.Errorf("stale capture dirs remain: %v", matches)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("unrelated dir was removed: %v", err)
	}

	// Idempotent: a second sweep with nothing stale removes nothing.
	if n, err := SweepStaleTempDirs(dir, logger); err != nil || n != 0 {
		t.Errorf("second sweep = (%d, %v), want (0, nil)", n, err)
	}
}
