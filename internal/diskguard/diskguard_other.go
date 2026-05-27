//go:build !unix

package diskguard

import "math"

// FreeBytes is a no-op stub on non-Unix platforms. The bot container is
// always Linux in production; this exists so the package builds on
// developer Windows / macOS-as-non-unix machines without forcing
// build tags throughout the codebase.
func FreeBytes(_ string) (uint64, error) {
	return math.MaxUint64, nil
}
