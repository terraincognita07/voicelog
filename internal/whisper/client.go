package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type Client struct {
	URL    string
	HTTP   *http.Client
	Logger *slog.Logger // optional; only used for warn-once on missing segments

	// toWAV converts srcPath to a 16 kHz mono WAV at dst. Default
	// implementation shells out to ffmpeg; tests inject a stub so the
	// outer Transcribe can be exercised without the binary on PATH.
	// Unexported so the public API stays small.
	toWAV func(ctx context.Context, src, dst string) error

	noSegmentsWarnOnce sync.Once
}

// New returns a default Client. Logger stays nil — set it via the
// public field if you want the warn-once "no segments" notice. The
// bot and the MCP retranscribe tool both wire their respective
// loggers in cmd/{bot,mcp}/main.go after calling whisper.New.
func New(url string) *Client {
	return &Client{
		URL:   url,
		HTTP:  &http.Client{Timeout: 10 * time.Minute},
		toWAV: ffmpegToWAV,
	}
}

// Segment is a single whisper inference window. We rely on two
// per-segment fields documented by whisper.cpp's verbose_json:
//
//	avg_logprob:    log-prob of the chosen tokens, ≤ 0; closer to 0 =
//	                more confident. Mean across segments → overall
//	                confidence; min across segments → worst patch.
//	no_speech_prob: probability the segment contains no speech. When
//	                whisper hallucinates plausible text on silence /
//	                music, the first segment usually has a high value
//	                (typically > 0.6).
//
// Older whisper servers and the plain `json` format omit these fields;
// we parse defensively and let callers treat missing data as "unknown"
// instead of "perfect".
type Segment struct {
	AvgLogprob   float64 `json:"avg_logprob"`
	NoSpeechProb float64 `json:"no_speech_prob"`
}

// Result is the parsed whisper response. Segments is nil when the
// server replied with the plain `json` format or with an empty list —
// callers MUST treat that as "no per-segment data" and skip confidence
// aggregation rather than reporting a misleading 0.0.
type Result struct {
	Text     string    `json:"text"`
	Segments []Segment `json:"segments"`
}

// Transcribe converts srcPath (any ffmpeg-supported audio) to 16 kHz mono WAV,
// POSTs it to /inference, and returns the parsed result. prompt is the
// optional whisper "initial prompt" — a free-form hint string (names,
// jargon) that biases the decoder. Empty prompt = no hint.
//
// The request uses response_format=verbose_json so we get per-segment
// metadata. Servers that don't recognize that value will fall back to
// plain JSON; the parser tolerates either.
func (c *Client) Transcribe(ctx context.Context, srcPath, prompt string) (Result, error) {
	conv := c.toWAV
	if conv == nil {
		// Defensive default: hand-built Clients (tests, retranscribe) that
		// skipped New() still get a working converter.
		conv = ffmpegToWAV
	}
	wavPath := srcPath + ".wav"
	// Register cleanup before the convert call: a failed ffmpeg run can
	// still leave a partial/empty .wav behind. This matters most on the
	// MCP retranscribe path, which converts in-place under /data/audio
	// where ScanOrphans only catches *.oga (not *.oga.wav), so a leaked
	// partial would accumulate. os.Remove of a never-created file no-ops.
	defer os.Remove(wavPath)
	if err := conv(ctx, srcPath, wavPath); err != nil {
		return Result{}, fmt.Errorf("ffmpeg convert: %w", err)
	}
	return c.transcribeWAV(ctx, wavPath, prompt)
}

func ffmpegToWAV(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y", "-loglevel", "error",
		"-i", src,
		"-ar", "16000", "-ac", "1", "-f", "wav",
		dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func (c *Client) transcribeWAV(ctx context.Context, path, prompt string) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("open wav: %w", err)
	}
	defer f.Close()

	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	part, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return Result{}, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return Result{}, err
	}
	if err := mw.WriteField("response_format", "verbose_json"); err != nil {
		return Result{}, err
	}
	if prompt != "" {
		if err := mw.WriteField("prompt", prompt); err != nil {
			return Result{}, err
		}
	}
	if err := mw.Close(); err != nil {
		return Result{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, buf)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("post %s: %w", c.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Result{}, fmt.Errorf("whisper http %d: %s", resp.StatusCode, body)
	}

	var r Result
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return Result{}, fmt.Errorf("decode json: %w", err)
	}
	// One-time operator notice: if the server replied without per-segment
	// metadata, confidence detection is silently disabled for every
	// subsequent transcription. Most often this means whisper.cpp was
	// started without response_format support for verbose_json or with
	// an older build. Logging once avoids drowning the bot's slog stream.
	if len(r.Segments) == 0 && c.Logger != nil {
		c.noSegmentsWarnOnce.Do(func() {
			c.Logger.Warn("whisper response has no segments — confidence detection disabled for this process",
				"url", c.URL)
		})
	}
	return r, nil
}

// Aggregate computes the confidence summary from a Result. Returns zero
// values + ok=false when there are no segments — callers should record
// NULL in the DB rather than 0.0.
//
// overall = mean of avg_logprob across segments.
// worst   = min avg_logprob (the patch you'd review first).
// suspect = first segment's no_speech_prob > threshold.
func (r Result) Aggregate(threshold float64) (overall, worst float64, suspect bool, ok bool) {
	if len(r.Segments) == 0 {
		return 0, 0, false, false
	}
	worst = r.Segments[0].AvgLogprob
	sum := 0.0
	for _, s := range r.Segments {
		sum += s.AvgLogprob
		if s.AvgLogprob < worst {
			worst = s.AvgLogprob
		}
	}
	overall = sum / float64(len(r.Segments))
	suspect = r.Segments[0].NoSpeechProb > threshold
	return overall, worst, suspect, true
}
