package mcp

import (
	"encoding/json"
	"testing"

	"github.com/terraincognita07/voicelog/internal/db"
)

// TestMCPNote_AlwaysCarriesConfidenceFields guards docs/MCP.md's promise that
// every note carries confidence_overall / confidence_min (null when unknown)
// and suspect_hallucination (a real bool). It decodes the wire bytes into a
// generic map — the check a struct round-trip test is structurally blind to —
// so reintroducing `omitempty` (which drops nil pointers and false bools) fails
// here instead of silently shrinking the published contract.
func TestMCPNote_AlwaysCarriesConfidenceFields(t *testing.T) {
	// A plain text note: no confidence signals, not suspect → the worst case
	// for omitempty, since every one of the three fields is zero-valued.
	raw, err := json.Marshal(toMCP(db.Note{ID: 1, RawText: "x"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"confidence_overall", "confidence_min", "suspect_hallucination"} {
		if _, ok := m[k]; !ok {
			t.Errorf("field %q absent on the wire — contract drift vs docs/MCP.md", k)
		}
	}
	if m["confidence_overall"] != nil {
		t.Errorf("confidence_overall = %v, want null", m["confidence_overall"])
	}
	if m["suspect_hallucination"] != false {
		t.Errorf("suspect_hallucination = %v, want false", m["suspect_hallucination"])
	}
}
