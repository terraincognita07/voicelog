package whisper

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for the outer Transcribe — the wrapper that converts srcPath to
// WAV (via ffmpeg in prod) and then hands it to transcribeWAV. We swap
// the toWAV field with stubs so the binary isn't required on the test
// host. transcribeWAV itself is covered by client_test.go; here we
// assert the wiring: the converter runs, its output WAV is the file
// uploaded to the server, the WAV is removed after the call, and a
// converter error short-circuits without ever hitting the HTTP layer.

// stubConverter writes `body` into dst and records every invocation
// (src/dst pairs) into calls. Returning a non-nil err lets a test exercise
// the failure path.
type stubConverter struct {
	body  []byte
	err   error
	calls []convCall
}

type convCall struct {
	src, dst string
}

func (s *stubConverter) convert(_ context.Context, src, dst string) error {
	s.calls = append(s.calls, convCall{src: src, dst: dst})
	if s.err != nil {
		return s.err
	}
	return os.WriteFile(dst, s.body, 0o600)
}

func TestTranscribe_StubConverterHandsWAVToServer(t *testing.T) {
	// Happy path: stub writes "FAKE-WAV-BYTES" into the wav file, server
	// asserts the upload arrived. The end-to-end wiring is what we cover
	// here — transcribeWAV's behavior is exercised separately.
	body, _ := json.Marshal(Result{
		Text:     "ok",
		Segments: []Segment{{AvgLogprob: -0.1, NoSpeechProb: 0.0}},
	})
	var cap capturedRequest
	srv := fakeServer(t, http.StatusOK, string(body), &cap)
	stub := &stubConverter{body: []byte("FAKE-WAV-BYTES")}
	c := &Client{URL: srv.URL, HTTP: srv.Client(), toWAV: stub.convert}

	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "src.oga")
	if err := os.WriteFile(src, []byte("not really an oga"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	r, err := c.Transcribe(context.Background(), src, "hint")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if r.Text != "ok" {
		t.Errorf("text: want ok, got %q", r.Text)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("converter called %d times, want 1", len(stub.calls))
	}
	gotCall := stub.calls[0]
	if gotCall.src != src {
		t.Errorf("converter src = %q, want %q", gotCall.src, src)
	}
	if gotCall.dst != src+".wav" {
		t.Errorf("converter dst = %q, want %q (Transcribe appends .wav)", gotCall.dst, src+".wav")
	}
	if !cap.hasFile {
		t.Errorf("server did not receive a file part")
	}
	if cap.prompt != "hint" {
		t.Errorf("server prompt = %q, want %q", cap.prompt, "hint")
	}
}

func TestTranscribe_RemovesWAVAfterCall(t *testing.T) {
	// The WAV is a temp by-product. Transcribe must clean it up — both
	// on success AND on http error. Otherwise long-running processes
	// (the bot) leak tmp files at the rate of one per voice message.
	body, _ := json.Marshal(Result{Text: "ok", Segments: []Segment{{AvgLogprob: -0.1}}})
	var cap capturedRequest
	srv := fakeServer(t, http.StatusOK, string(body), &cap)

	stub := &stubConverter{body: []byte("data")}
	c := &Client{URL: srv.URL, HTTP: srv.Client(), toWAV: stub.convert}

	src := filepath.Join(t.TempDir(), "src.oga")
	if err := os.WriteFile(src, []byte("any"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if _, err := c.Transcribe(context.Background(), src, ""); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if _, err := os.Stat(src + ".wav"); !os.IsNotExist(err) {
		t.Errorf("WAV must be deleted after success; Stat err=%v", err)
	}
}

func TestTranscribe_ConverterErrorShortCircuits(t *testing.T) {
	// A converter failure (ffmpeg crash, malformed input) must surface
	// as "ffmpeg convert: ..." and must NOT hit the network — otherwise
	// every bad audio file racks up wasted whisper calls.
	srv := fakeServer(t, http.StatusOK, `{"text":"unreachable"}`, &capturedRequest{})

	stub := &stubConverter{err: errors.New("simulated ffmpeg failure")}
	// Wire HTTP at a host that would fail loudly if Transcribe reached
	// out to it — but the converter errors out first so we never get
	// there. srv.URL is captured only to keep the Client well-formed.
	c := &Client{URL: srv.URL, HTTP: srv.Client(), toWAV: stub.convert}

	src := filepath.Join(t.TempDir(), "src.oga")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	_, err := c.Transcribe(context.Background(), src, "")
	if err == nil {
		t.Fatal("Transcribe should surface converter error")
	}
	if !strings.Contains(err.Error(), "ffmpeg convert") {
		t.Errorf("err = %v, want 'ffmpeg convert: ...' prefix", err)
	}
	if !strings.Contains(err.Error(), "simulated ffmpeg failure") {
		t.Errorf("err must wrap the converter's err: %v", err)
	}
	// No wav file should be produced — the converter never wrote one.
	if _, statErr := os.Stat(src + ".wav"); !os.IsNotExist(statErr) {
		t.Errorf("no WAV expected when converter fails; got Stat err=%v", statErr)
	}
}

func TestTranscribe_NilToWAVFieldFallsBackToFFmpeg(t *testing.T) {
	// The defensive nil-check inside Transcribe lets hand-built Clients
	// (the few in tests, the RetranscribeDeps construction in MCP) keep
	// working without explicitly assigning toWAV. We can't call real
	// ffmpeg here — assert the fallback by pointing at a bogus path so
	// the real ffmpeg fails fast and we see the "ffmpeg convert:"
	// wrapper instead of a nil-function panic.
	c := &Client{URL: "http://unused.invalid", HTTP: http.DefaultClient}
	src := filepath.Join(t.TempDir(), "definitely-not-audio")
	if err := os.WriteFile(src, []byte("xx"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	_, err := c.Transcribe(context.Background(), src, "")
	if err == nil {
		t.Fatal("expected ffmpeg failure (or nil-toWAV panic)")
	}
	if !strings.Contains(err.Error(), "ffmpeg convert") {
		t.Errorf("err = %v, want 'ffmpeg convert: ...' (fallback engaged)", err)
	}
}
