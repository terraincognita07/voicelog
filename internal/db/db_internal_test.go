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

func TestChunkIDs(t *testing.T) {
	cases := []struct {
		name string
		ids  []int64
		size int
		want [][]int64
	}{
		{"empty", nil, 500, nil},
		{"under size", []int64{1, 2, 3}, 500, [][]int64{{1, 2, 3}}},
		{"exact size", []int64{1, 2}, 2, [][]int64{{1, 2}}},
		{"over size splits", []int64{1, 2, 3, 4, 5}, 2, [][]int64{{1, 2}, {3, 4}, {5}}},
		{"exact multiple", []int64{1, 2, 3, 4}, 2, [][]int64{{1, 2}, {3, 4}}},
		{"size below one clamps to one", []int64{1, 2}, 0, [][]int64{{1}, {2}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := chunkIDs(c.ids, c.size); !equalChunks(got, c.want) {
				t.Errorf("chunkIDs(%v, %d) = %v, want %v", c.ids, c.size, got, c.want)
			}
		})
	}

	// Property that keeps every IN-clause under SQLite's bind-variable
	// ceiling: no chunk may exceed maxBatchIDs for a large input.
	big := make([]int64, maxBatchIDs*2+7)
	for _, ch := range chunkIDs(big, maxBatchIDs) {
		if len(ch) > maxBatchIDs {
			t.Fatalf("chunk of %d exceeds maxBatchIDs %d", len(ch), maxBatchIDs)
		}
	}
}

func TestIDPlaceholders(t *testing.T) {
	marks, args := idPlaceholders([]int64{10, 20, 30})
	if marks != "?,?,?" {
		t.Errorf("marks = %q, want ?,?,?", marks)
	}
	if len(args) != 3 || args[0] != int64(10) || args[1] != int64(20) || args[2] != int64(30) {
		t.Errorf("args = %v, want [10 20 30]", args)
	}
}

func equalChunks(a, b [][]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}
