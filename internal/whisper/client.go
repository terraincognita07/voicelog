package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Client struct {
	URL  string
	HTTP *http.Client
}

func New(url string) *Client {
	return &Client{
		URL:  url,
		HTTP: &http.Client{Timeout: 10 * time.Minute},
	}
}

type Result struct {
	Text string `json:"text"`
}

// Transcribe converts srcPath (any ffmpeg-supported audio) to 16 kHz mono WAV,
// POSTs it to /inference, and returns the transcribed text.
func (c *Client) Transcribe(ctx context.Context, srcPath string) (string, error) {
	wavPath := srcPath + ".wav"
	if err := toWAV(ctx, srcPath, wavPath); err != nil {
		return "", fmt.Errorf("ffmpeg convert: %w", err)
	}
	defer os.Remove(wavPath)
	return c.transcribeWAV(ctx, wavPath)
}

func toWAV(ctx context.Context, src, dst string) error {
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

func (c *Client) transcribeWAV(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open wav: %w", err)
	}
	defer f.Close()

	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	part, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	if err := mw.WriteField("response_format", "json"); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("post %s: %w", c.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("whisper http %d: %s", resp.StatusCode, body)
	}

	var r Result
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("decode json: %w", err)
	}
	return r.Text, nil
}
