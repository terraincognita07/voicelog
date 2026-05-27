package diskguard

import (
	"testing"
)

// TestFreeBytes_NonZeroOnExistingDir is the platform-agnostic
// contract: passing an existing directory must return a non-zero
// free-bytes value with no error. On Unix that's the real Statfs
// number; on non-Unix it's the math.MaxUint64 sentinel — both are
// > 0 by construction.
func TestFreeBytes_NonZeroOnExistingDir(t *testing.T) {
	dir := t.TempDir()
	got, err := FreeBytes(dir)
	if err != nil {
		t.Fatalf("FreeBytes(%q): unexpected err %v", dir, err)
	}
	if got == 0 {
		t.Errorf("FreeBytes(%q) = 0, want > 0", dir)
	}
}
