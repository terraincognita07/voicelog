package db

import (
	"errors"
	"strings"
	"testing"
)

func TestStemCyrillicQuery(t *testing.T) {
	cases := []struct {
		name  string
		query string
		check func(t *testing.T, got string)
	}{
		{"latin untouched", "cat dog", func(t *testing.T, got string) {
			if got != "cat dog" {
				t.Errorf("latin must be untouched, got %q", got)
			}
		}},
		{"cyrillic becomes prefix", "работа", func(t *testing.T, got string) {
			if !strings.HasSuffix(got, "*") || strings.Contains(got, " ") {
				t.Errorf("expected single stemmed prefix token, got %q", got)
			}
			if !strings.HasPrefix(got, "работ") {
				t.Errorf("stem of 'работа' should start with 'работ', got %q", got)
			}
		}},
		{"mixed keeps latin precise", "работа cat", func(t *testing.T, got string) {
			parts := strings.Fields(got)
			if len(parts) != 2 || parts[1] != "cat" {
				t.Errorf("latin token must stay exact, got %q", got)
			}
			if !strings.HasSuffix(parts[0], "*") {
				t.Errorf("cyrillic token must be a prefix, got %q", got)
			}
		}},
		{"operator OR preserved", "работа OR дом", func(t *testing.T, got string) {
			if !strings.Contains(got, " OR ") {
				t.Errorf("FTS5 operator OR must survive, got %q", got)
			}
		}},
		{"already wildcard untouched", "работ*", func(t *testing.T, got string) {
			if got != "работ*" {
				t.Errorf("existing wildcard must be untouched, got %q", got)
			}
		}},
		{"quoted phrase skipped", `"моя работа"`, func(t *testing.T, got string) {
			if got != `"моя работа"` {
				t.Errorf("quoted phrase must be untouched, got %q", got)
			}
		}},
		{"punctuation token skipped", "Колю,", func(t *testing.T, got string) {
			if got != "Колю," {
				t.Errorf("token with punctuation must be untouched, got %q", got)
			}
		}},
		{"empty", "", func(t *testing.T, got string) {
			if got != "" {
				t.Errorf("empty stays empty, got %q", got)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.check(t, stemCyrillicQuery(c.query)) })
	}
}

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
