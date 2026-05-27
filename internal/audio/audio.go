// Package audio implements opt-in retention of the raw voice files
// transcribed by the bot.
//
// Default: OFF. Activated by AUDIO_RETENTION_DAYS > 0 in cmd/bot/main.go.
// When on, the bot copies the original Telegram .oga (Opus) file to
// <dir>/<note_id>.oga after a successful transcription + DB insert, and
// records the path in notes.audio_path. A background janitor sweeps
// every JanitorPeriod, deletes files older than the retention window,
// and nulls audio_path for those notes.
//
// Threat model boundary: retained audio is sensitive at rest. The bot
// host's filesystem is the trust boundary — no encryption, no per-file
// ACLs beyond Linux uid. Self-host, single-user assumed.
package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"voicelog/internal/db"
)

// JanitorPeriod is the cleanup tick. Six hours is a sane compromise:
// keeps disk pressure visible-ish without spamming syscalls. The first
// sweep fires one period after startup so we don't deadlock with the
// initial bot init.
const JanitorPeriod = 6 * time.Hour

// SaveOriginal copies src to dst/<id>.oga. dst is created if missing.
// File permissions: 0600 (owner read+write only). The on-disk extension
// is fixed as .oga regardless of source extension — Telegram voice
// messages are always Opus-in-OGG.
func SaveOriginal(srcPath, dir string, id int64) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir audio dir: %w", err)
	}
	destPath := filepath.Join(dir, strconv.FormatInt(id, 10)+".oga")
	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("open src: %w", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("open dst: %w", err)
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		// Best-effort cleanup so we don't leave a half-written file.
		_ = os.Remove(destPath)
		return "", fmt.Errorf("copy audio: %w", err)
	}
	return destPath, nil
}

// Janitor runs until ctx is cancelled, performing periodic cleanup of
// retained audio files that have aged past retentionDays.
//
// Safety rails:
//   - Files are deleted only if their on-disk path lies inside `dir` (no
//     traversal). A stored path that escapes via "../" is ignored — the
//     DB row is still nulled so the next pass doesn't waste a syscall.
//   - DB nulling and file removal are independent: if the file is already
//     gone, we still clear audio_path; if the delete fails we keep the
//     row pointing at it so the next pass can retry.
//   - On any DB error the loop continues — a transient failure should
//     not stop future passes.
func Janitor(ctx context.Context, store *db.DB, dir string, retentionDays int, logger *slog.Logger) {
	if retentionDays <= 0 || dir == "" {
		return
	}
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		logger.Error("audio janitor: bad dir", "dir", dir, "err", err)
		return
	}

	tick := time.NewTicker(JanitorPeriod)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			runOnce(ctx, store, dirAbs, retentionDays, logger)
		}
	}
}

func runOnce(ctx context.Context, store *db.DB, dirAbs string, retentionDays int, logger *slog.Logger) {
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	refs, err := store.AudiosOlderThan(ctx, cutoff)
	if err != nil {
		logger.Error("audio janitor: list", "err", err)
		return
	}
	deleted := 0
	for _, r := range refs {
		if !pathInside(dirAbs, r.Path) {
			// Defensive: drop the DB pointer; do NOT touch the file.
			logger.Warn("audio janitor: skipping path outside managed dir",
				"id", r.ID, "path", r.Path)
			_ = store.ClearAudioPath(ctx, r.ID)
			continue
		}
		if err := os.Remove(r.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Warn("audio janitor: remove failed", "id", r.ID, "path", r.Path, "err", err)
			continue
		}
		if err := store.ClearAudioPath(ctx, r.ID); err != nil {
			logger.Warn("audio janitor: clear audio_path", "id", r.ID, "err", err)
			continue
		}
		deleted++
	}
	if deleted > 0 {
		logger.Info("audio janitor: pass complete", "deleted", deleted, "cutoff", cutoff.Format(time.RFC3339))
	}
}

// pathInside returns true iff p resolves (without following symlinks)
// to a location strictly under dir. dir must already be absolute.
func pathInside(dir, p string) bool {
	pAbs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, pAbs)
	if err != nil {
		return false
	}
	// rel must not start with ".." and must not be ".".
	if rel == "." || rel == "" {
		return false
	}
	if len(rel) >= 2 && rel[:2] == ".." {
		return false
	}
	return true
}
