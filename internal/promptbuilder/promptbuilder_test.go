package promptbuilder

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

type fakeVocab struct {
	val string
	err error
}

func (f fakeVocab) VocabPrompt(ctx context.Context) (string, error) {
	return f.val, f.err
}

func TestCompose(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		base       string
		vocab      string
		vocabErr   error
		want       string
		wantLogged bool
	}{
		{"both_empty", "", "", nil, "", false},
		{"base_only", "помни о ", "", nil, "помни о", false}, // trimmed
		{"vocab_only", "", "Иннокентий, Глафира", nil, "Иннокентий, Глафира", false},
		{"base_and_vocab", "  Glossary:", "Иннокентий", nil, "Glossary: Иннокентий", false},
		{"vocab_err_falls_back_to_base", "hello", "", errors.New("db down"), "hello", true},
		{"vocab_err_with_empty_base", "", "", errors.New("db down"), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			got := Compose(ctx, fakeVocab{val: c.vocab, err: c.vocabErr}, c.base, logger)
			if got != c.want {
				t.Errorf("Compose: want %q, got %q", c.want, got)
			}
			logged := strings.Contains(buf.String(), "vocab prompt")
			if logged != c.wantLogged {
				t.Errorf("warn-log expected=%v got=%v (log: %q)", c.wantLogged, logged, buf.String())
			}
		})
	}
}

// TestCompose_NilLoggerSafe: callers may pass nil logger; an err in
// VocabPrompt must not panic in that case.
func TestCompose_NilLoggerSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on nil logger: %v", r)
		}
	}()
	got := Compose(context.Background(),
		fakeVocab{err: errors.New("db down")},
		"base text", nil)
	if got != "base text" {
		t.Errorf("want fallback to base, got %q", got)
	}
}
