package db_test

import (
	"context"
	"strings"
	"testing"
)

func TestVocabCRUD(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	// Empty.
	terms, err := d.ListVocab(ctx)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(terms) != 0 {
		t.Fatalf("want empty, got %v", terms)
	}
	if p, _ := d.VocabPrompt(ctx); p != "" {
		t.Fatalf("want empty prompt, got %q", p)
	}

	// Add.
	for _, term := range []string{"Иннокентий", "Сбербанк", "voicelog"} {
		added, err := d.AddVocab(ctx, term)
		if err != nil {
			t.Fatalf("add %s: %v", term, err)
		}
		if !added {
			t.Fatalf("add %s reported not-added on fresh table", term)
		}
	}

	// Whitespace trim + empty input.
	if added, _ := d.AddVocab(ctx, "   "); added {
		t.Fatalf("blank input must not insert a row")
	}

	// NOCASE duplicate is silently ignored.
	added, err := d.AddVocab(ctx, "ИННОКЕНТИЙ")
	if err != nil {
		t.Fatalf("add dup: %v", err)
	}
	if added {
		t.Fatalf("case-different duplicate should be ignored, got added=true")
	}

	terms, _ = d.ListVocab(ctx)
	if len(terms) != 3 {
		t.Fatalf("want 3 terms, got %d: %v", len(terms), terms)
	}

	prompt, _ := d.VocabPrompt(ctx)
	if !strings.Contains(prompt, "Иннокентий") || !strings.Contains(prompt, "voicelog") {
		t.Fatalf("prompt missing terms: %q", prompt)
	}

	// Remove (NOCASE).
	removed, err := d.RemoveVocab(ctx, "сбербанк")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed {
		t.Fatalf("remove NOCASE should match Сбербанк")
	}
	removed, _ = d.RemoveVocab(ctx, "doesnotexist")
	if removed {
		t.Fatalf("removing absent term must return false")
	}

	// Clear.
	n, err := d.ClearVocab(ctx)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n != 2 {
		t.Fatalf("clear count: want 2, got %d", n)
	}
	terms, _ = d.ListVocab(ctx)
	if len(terms) != 0 {
		t.Fatalf("want empty after clear, got %v", terms)
	}
}
