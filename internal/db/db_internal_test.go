package db

import (
	"errors"
	"testing"
)

func TestIsSQLiteBusy(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("some other error"), false},
		{"busy_full_modernc", errors.New("database is locked (5) (SQLITE_BUSY)"), true},
		{"busy_substring_only", errors.New("wrapped: SQLITE_BUSY at startup"), true},
		{"locked_without_marker", errors.New("file is locked"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSQLiteBusy(c.err); got != c.want {
				t.Errorf("isSQLiteBusy(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
