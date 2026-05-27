//go:build !unix

package diskguard

import (
	"math"
	"testing"
)

// TestFreeBytes_NonUnix_AlwaysSentinel asserts the no-op stub
// returns math.MaxUint64 with no error for any input. That sentinel
// is what makes the bot tolerate dev builds on Windows/macOS-as-non-
// unix — any non-zero threshold check passes.
func TestFreeBytes_NonUnix_AlwaysSentinel(t *testing.T) {
	cases := []string{
		`C:\tmp`,
		"/data/audio",
		"",
		"definitely-not-a-real-path",
	}
	for _, c := range cases {
		got, err := FreeBytes(c)
		if err != nil {
			t.Errorf("FreeBytes(%q): want nil err, got %v", c, err)
		}
		if got != math.MaxUint64 {
			t.Errorf("FreeBytes(%q): want MaxUint64, got %d", c, got)
		}
	}
}
