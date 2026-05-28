package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResult_Aggregate(t *testing.T) {
	cases := []struct {
		name        string
		segments    []Segment
		thresh      float64
		wantOverall float64
		wantWorst   float64
		wantSuspect bool
		wantOK      bool
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
			wantOverall: -0.9, // mean of -1.5 and -0.3
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

// --- HTTP path (transcribeWAV) ------------------------------------------

// writeTempWAV creates a small file the test can hand to transcribeWAV.
// Content doesn't matter — the server we wire up below ignores it.
func writeTempWAV(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.wav")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write wav: %v", err)
	}
	return path
}

// capturedRequest is what the fake whisper server records on each call.
type capturedRequest struct {
	contentType string
	prompt      string
	respFormat  string
	hasPrompt   bool
	hasFile     bool
}

// fakeServer wraps an httptest.Server with a captured-request slot and
// a customizable response. The handler parses the multipart body so
// tests can assert on the form fields the client sent.
func fakeServer(t *testing.T, status int, body string, captured *capturedRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.contentType = r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			http.Error(w, "bad multipart: "+err.Error(), http.StatusBadRequest)
			return
		}
		if v, ok := r.MultipartForm.Value["response_format"]; ok && len(v) > 0 {
			captured.respFormat = v[0]
		}
		if v, ok := r.MultipartForm.Value["prompt"]; ok && len(v) > 0 {
			captured.prompt = v[0]
			captured.hasPrompt = true
		}
		if files, ok := r.MultipartForm.File["file"]; ok && len(files) > 0 {
			captured.hasFile = true
			f, err := files[0].Open()
			if err == nil {
				_, _ = io.Copy(io.Discard, f)
				_ = f.Close()
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTranscribeWAV_VerboseJSONSuccess(t *testing.T) {
	body, _ := json.Marshal(Result{
		Text: "купить молоко",
		Segments: []Segment{
			{AvgLogprob: -0.21, NoSpeechProb: 0.05},
			{AvgLogprob: -0.35, NoSpeechProb: 0.10},
		},
	})
	var cap capturedRequest
	srv := fakeServer(t, http.StatusOK, string(body), &cap)
	c := &Client{URL: srv.URL, HTTP: srv.Client()}

	wav := writeTempWAV(t, "RIFF....fake wav")
	r, err := c.transcribeWAV(context.Background(), wav, "Иннокентий")
	if err != nil {
		t.Fatalf("transcribeWAV: %v", err)
	}
	if r.Text != "купить молоко" {
		t.Errorf("text: %q", r.Text)
	}
	if len(r.Segments) != 2 {
		t.Fatalf("segments len = %d, want 2", len(r.Segments))
	}
	if !cap.hasFile {
		t.Errorf("server didn't see a file part")
	}
	if !strings.HasPrefix(cap.contentType, "multipart/form-data") {
		t.Errorf("content-type: %q", cap.contentType)
	}
	if cap.respFormat != "verbose_json" {
		t.Errorf("response_format: %q, want verbose_json", cap.respFormat)
	}
	if !cap.hasPrompt || cap.prompt != "Иннокентий" {
		t.Errorf("prompt field: hasPrompt=%v value=%q", cap.hasPrompt, cap.prompt)
	}
}

func TestTranscribeWAV_PlainJSONNoSegments(t *testing.T) {
	// Older whisper servers / response_format=json: no `segments` key.
	// Decoder must succeed; Aggregate must return ok=false.
	body := `{"text":"hello world"}`
	var cap capturedRequest
	srv := fakeServer(t, http.StatusOK, body, &cap)
	c := &Client{URL: srv.URL, HTTP: srv.Client()}

	r, err := c.transcribeWAV(context.Background(), writeTempWAV(t, "x"), "")
	if err != nil {
		t.Fatalf("transcribeWAV: %v", err)
	}
	if r.Text != "hello world" {
		t.Errorf("text: %q", r.Text)
	}
	if len(r.Segments) != 0 {
		t.Errorf("segments: want 0, got %d", len(r.Segments))
	}
	_, _, _, ok := r.Aggregate(0.6)
	if ok {
		t.Errorf("Aggregate must return ok=false on missing segments")
	}
}

// TestTranscribeWAV_WarnsOnceOnMissingSegments verifies the operator
// notice fires exactly once per *Client lifetime when the server
// keeps returning responses without per-segment metadata.
func TestTranscribeWAV_WarnsOnceOnMissingSegments(t *testing.T) {
	var cap capturedRequest
	srv := fakeServer(t, http.StatusOK, `{"text":"silent"}`, &cap)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	c := &Client{URL: srv.URL, HTTP: srv.Client(), Logger: logger}

	for i := 0; i < 3; i++ {
		if _, err := c.transcribeWAV(context.Background(), writeTempWAV(t, "x"), ""); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	got := strings.Count(buf.String(), "no segments")
	if got != 1 {
		t.Errorf("warn-once: want exactly 1 'no segments' log entry across 3 calls, got %d (full log: %q)", got, buf.String())
	}
}

// TestTranscribeWAV_DoesNotWarnOnSegmentsPresent: the warn-once must
// stay quiet when the server replies with segments. Regression guard
// against a refactor that triggers the once even on the success path.
func TestTranscribeWAV_DoesNotWarnOnSegmentsPresent(t *testing.T) {
	body, _ := json.Marshal(Result{
		Text:     "ok",
		Segments: []Segment{{AvgLogprob: -0.1, NoSpeechProb: 0.0}},
	})
	var cap capturedRequest
	srv := fakeServer(t, http.StatusOK, string(body), &cap)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	c := &Client{URL: srv.URL, HTTP: srv.Client(), Logger: logger}

	if _, err := c.transcribeWAV(context.Background(), writeTempWAV(t, "x"), ""); err != nil {
		t.Fatalf("call: %v", err)
	}
	if strings.Contains(buf.String(), "no segments") {
		t.Errorf("must not warn when segments are present: %q", buf.String())
	}
}

func TestTranscribeWAV_OmitsPromptWhenEmpty(t *testing.T) {
	var cap capturedRequest
	srv := fakeServer(t, http.StatusOK, `{"text":""}`, &cap)
	c := &Client{URL: srv.URL, HTTP: srv.Client()}

	_, err := c.transcribeWAV(context.Background(), writeTempWAV(t, "x"), "")
	if err != nil {
		t.Fatalf("transcribeWAV: %v", err)
	}
	if cap.hasPrompt {
		t.Errorf("prompt field must be absent when prompt arg is empty")
	}
}

func TestTranscribeWAV_HTTPError(t *testing.T) {
	var cap capturedRequest
	srv := fakeServer(t, http.StatusInternalServerError, "model OOM", &cap)
	c := &Client{URL: srv.URL, HTTP: srv.Client()}

	_, err := c.transcribeWAV(context.Background(), writeTempWAV(t, "x"), "")
	if err == nil {
		t.Fatalf("want error on HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err should mention status code, got: %v", err)
	}
}

func TestTranscribeWAV_BadJSON(t *testing.T) {
	var cap capturedRequest
	srv := fakeServer(t, http.StatusOK, "not json at all", &cap)
	c := &Client{URL: srv.URL, HTTP: srv.Client()}

	_, err := c.transcribeWAV(context.Background(), writeTempWAV(t, "x"), "")
	if err == nil {
		t.Fatalf("want decode error on garbage body")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("err should mention decode, got: %v", err)
	}
}

func TestTranscribeWAV_MissingFile(t *testing.T) {
	// Server is never reached — file open fails first.
	c := &Client{URL: "http://unused.invalid", HTTP: http.DefaultClient}
	_, err := c.transcribeWAV(context.Background(), filepath.Join(t.TempDir(), "no-such.wav"), "")
	if err == nil {
		t.Fatalf("want error opening missing wav, got nil")
	}
	if !strings.Contains(err.Error(), "open wav") {
		t.Errorf("err should mention 'open wav', got: %v", err)
	}
}
