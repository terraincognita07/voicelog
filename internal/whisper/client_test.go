package whisper

import (
	"testing"
)

func TestResult_Aggregate(t *testing.T) {
	cases := []struct {
		name         string
		segments     []Segment
		thresh       float64
		wantOverall  float64
		wantWorst    float64
		wantSuspect  bool
		wantOK       bool
	}{
		{
			name:     "no segments",
			segments: nil,
			thresh:   0.6,
			wantOK:   false,
		},
		{
			name: "single segment, high confidence, speech",
			segments: []Segment{
				{AvgLogprob: -0.2, NoSpeechProb: 0.1},
			},
			thresh:      0.6,
			wantOverall: -0.2,
			wantWorst:   -0.2,
			wantSuspect: false,
			wantOK:      true,
		},
		{
			name: "first segment looks like silence",
			segments: []Segment{
				{AvgLogprob: -1.5, NoSpeechProb: 0.85},
				{AvgLogprob: -0.3, NoSpeechProb: 0.05},
			},
			thresh:      0.6,
			wantOverall: -0.9,  // mean of -1.5 and -0.3
			wantWorst:   -1.5,
			wantSuspect: true,
			wantOK:      true,
		},
		{
			name: "first segment quiet but below threshold",
			segments: []Segment{
				{AvgLogprob: -0.5, NoSpeechProb: 0.55},
				{AvgLogprob: -0.2, NoSpeechProb: 0.1},
			},
			thresh:      0.6,
			wantOverall: -0.35,
			wantWorst:   -0.5,
			wantSuspect: false,
			wantOK:      true,
		},
		{
			name: "threshold customized lower → catches more",
			segments: []Segment{
				{AvgLogprob: -0.5, NoSpeechProb: 0.55},
			},
			thresh:      0.5,
			wantOverall: -0.5,
			wantWorst:   -0.5,
			wantSuspect: true,
			wantOK:      true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Result{Segments: c.segments}
			overall, worst, suspect, ok := r.Aggregate(c.thresh)
			if ok != c.wantOK {
				t.Fatalf("ok: want %v, got %v", c.wantOK, ok)
			}
			if !ok {
				return
			}
			if !floatNear(overall, c.wantOverall) {
				t.Errorf("overall: want %v, got %v", c.wantOverall, overall)
			}
			if !floatNear(worst, c.wantWorst) {
				t.Errorf("worst: want %v, got %v", c.wantWorst, worst)
			}
			if suspect != c.wantSuspect {
				t.Errorf("suspect: want %v, got %v", c.wantSuspect, suspect)
			}
		})
	}
}

func floatNear(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
