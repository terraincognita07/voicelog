//go:build unix

package diskguard

import "syscall"

// FreeBytes returns the bytes available to a non-privileged user on the
// filesystem that contains `dir`. It returns 0 + the syscall error if
// statfs fails (callers should treat that as "unknown" rather than
// "no space" and let the operation through).
func FreeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	// Bavail is for non-root callers; we run as a regular user in the
	// container so Bavail is the right measure (Bfree includes
	// reserved blocks we can't actually touch).
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
