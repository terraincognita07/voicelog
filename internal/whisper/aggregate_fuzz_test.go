package whisper

import (
	"encoding/binary"
	"math"
	"testing"
)

// Fuzz Result.Aggregate. The function consumes per-segment metadata
// (avg_logprob, no_speech_prob) sourced from whisper.cpp; in practice
// those are real-valued log-probabilities in [-∞, 0]. We encode the
// fuzz input as a flat byte slice where every 16 bytes form one
// segment (two float64s). This lets the fuzzer explore segment-count,
// odd values (NaN, ±Inf), and the empty case.

func decodeSegments(data []byte) []Segment {
	const sz = 16 // 2 × float64
	out := make([]Segment, 0, len(data)/sz)
	for len(data) >= sz {
		a := math.Float64frombits(binary.LittleEndian.Uint64(data[:8]))
		b := math.Float64frombits(binary.LittleEndian.Uint64(data[8:16]))
		out = append(out, Segment{AvgLogprob: a, NoSpeechProb: b})
		data = data[sz:]
	}
	return out
}

func FuzzResult_Aggregate(f *testing.F) {
	// Seed a few realistic shapes alongside the boundary cases.
	for _, seed := range [][]byte{
		nil,                  // 0 segments → ok=false
		make([]byte, 16),     // 1 zero-valued segment
		make([]byte, 32),     // 2 zero-valued segments
		make([]byte, 16*100), // long run
		make([]byte, 15),     // sub-segment trailing bytes ignored
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0, 0}, // NaN-ish
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		segs := decodeSegments(data)
		r := Result{Segments: segs}

		overall, worst, suspect, ok := r.Aggregate(0.6)

		// Contract checks.
		if len(segs) == 0 {
			if ok {
				t.Errorf("ok must be false for 0 segments")
			}
			if overall != 0 || worst != 0 || suspect {
				t.Errorf("zero-segment result must return zeroes: overall=%v worst=%v suspect=%v",
					overall, worst, suspect)
			}
			return
		}
		if !ok {
			t.Errorf("ok must be true for non-empty segments")
		}
		// worst is the min avg_logprob across segments. NaN comparisons
		// in Go always return false, which means a NaN in any position
		// can break the min — that's a real concern for whisper.cpp
		// output if it ever emits NaN. Document the current behavior:
		// if any seg is NaN, worst may also be NaN, but no panic.
		_ = overall
		_ = worst
		_ = suspect
	})
}
