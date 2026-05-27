//go:build unix

package diskguard

import (
	"math"
	"path/filepath"
	"testing"
)

// TestFreeBytes_Unix_RealisticRange confirms the value returned for a
// real temp dir is a real Statfs reading, not the non-Unix sentinel.
// math.MaxUint64 is what diskguard_other.go returns; on a Unix build
// hitting that exact value would mean the wrong file shipped.
func TestFreeBytes_Unix_RealisticRange(t *testing.T) {
	got, err := FreeBytes(t.TempDir())
	if err != nil {
		t.Fatalf("FreeBytes: %v", err)
	}
	if got == math.MaxUint64 {
		t.Errorf("FreeBytes returned the non-Unix sentinel on a Unix build: %d", got)
	}
}

// TestFreeBytes_Unix_BadPath asserts the syscall.Statfs error surface
// reaches the caller — important because the bot treats an error as
// "unknown" and lets the write through. If the function ever silently
// swallowed errors, a missing AUDIO_DIR would never be flagged.
func TestFreeBytes_Unix_BadPath(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no", "such", "subdir")
	_, err := FreeBytes(bogus)
	if err == nil {
		t.Fatalf("FreeBytes(%q): expected error, got nil", bogus)
	}
}
