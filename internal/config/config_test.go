package config

import (
	"bytes"
	"log/slog"
	"os"
	"testing"
)

func TestParseFloat01Inner(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		wantF    float64
		wantPres bool
		wantErr  bool
	}{
		{"empty", "", 0, false, false},
		{"zero", "0", 0, true, false},
		{"half", "0.5", 0.5, true, false},
		{"one", "1", 1, true, false},
		{"negative", "-0.1", 0, true, true},
		{"above_one", "1.5", 0, true, true},
		{"not_a_number", "foo", 0, true, true},
		{"empty_with_whitespace", " ", 0, true, true}, // strconv rejects " "
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, present, err := parseFloat01(c.value)
			if (err != nil) != c.wantErr {
				t.Errorf("err mismatch: want err=%v, got %v", c.wantErr, err)
			}
			if present != c.wantPres {
				t.Errorf("present: want %v, got %v", c.wantPres, present)
			}
			if !c.wantErr && f != c.wantF {
				t.Errorf("value: want %v, got %v", c.wantF, f)
			}
		})
	}
}

func TestParseFloat01_UnsetUsesFallback(t *testing.T) {
	t.Setenv("VOICELOG_TEST_F01", "")
	// Setenv with "" doesn't unset on all platforms; clear explicitly.
	os.Unsetenv("VOICELOG_TEST_F01")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	got := ParseFloat01(logger, "VOICELOG_TEST_F01", 0.42)
	if got != 0.42 {
		t.Errorf("want fallback 0.42, got %v", got)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no log output, got %q", buf.String())
	}
}

func TestParseFloat01_ValidUsesEnv(t *testing.T) {
	t.Setenv("VOICELOG_TEST_F01", "0.75")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	got := ParseFloat01(logger, "VOICELOG_TEST_F01", 0.42)
	if got != 0.75 {
		t.Errorf("want parsed 0.75, got %v", got)
	}
}
