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
	"runtime"
	"strconv"
	"time"

	"voicelog/internal/db"
)

// JanitorPeriod is the cleanup tick. Six hours is a sane compromise:
// keeps disk pressure visible-ish without spamming syscalls. The first
// sweep fires one period after startup so we don't deadlock with the
// initial bot init.
const JanitorPeriod = 6 * time.Hour

// SaveOriginal copies src to dir/<id>.oga. dir is created if missing.
// File permissions: 0600 (owner read+write only). The on-disk extension
// is fixed as .oga regardless of source extension — Telegram voice
// messages are always Opus-in-OGG.
//
// Returns the RELATIVE path (the basename, "<id>.oga"), not an absolute
// one. Callers should resolve it against the active audio dir via
// Resolve when they need to touch the file. Storing relative paths in
// notes.audio_path means a later AUDIO_DIR change no longer abandons
// retained files — the new dir is applied at read time. (Closes F3 for
// newly-written notes; legacy absolute rows keep working through
// Resolve's backward-compat branch.)
func SaveOriginal(srcPath, dir string, id int64) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir audio dir: %w", err)
	}
	rel := strconv.FormatInt(id, 10) + ".oga"
	destPath := filepath.Join(dir, rel)
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
	return rel, nil
}

// Resolve returns the absolute on-disk path for a value stored in
// notes.audio_path. Relative values (new format, post-F3) are joined
// under audioDir. Absolute values (legacy, pre-F3 rows) are returned
// as-is so they remain reachable even if AUDIO_DIR has changed since
// they were written.
func Resolve(audioDir, storedPath string) string {
	if storedPath == "" {
		return ""
	}
	if filepath.IsAbs(storedPath) {
		return storedPath
	}
	return filepath.Join(audioDir, storedPath)
}

// RelativizeLegacyPaths is a one-shot startup pass that rewrites legacy
// absolute audio_path values to the relative basename format, but only
// where the absolute path resolves under the current audioDir. Rows
// already in relative format are left alone (idempotent). Rows whose
// absolute path lies OUTSIDE the current dir are also left alone — we
// have nothing better to point them at, and the janitor's "outside
// managed dir" branch will null them on its next sweep.
//
// Returns the number of rows actually rewritten. Safe to call on every
// startup; safe to call concurrently with the janitor (each row's
// SetAudioPath is its own UPDATE).
func RelativizeLegacyPaths(ctx context.Context, store *db.DB, audioDir string) (int, error) {
	if audioDir == "" {
		return 0, nil
	}
	dirAbs, err := filepath.Abs(audioDir)
	if err != nil {
		return 0, fmt.Errorf("abs audio dir: %w", err)
	}
	refs, err := store.AllRetainedAudios(ctx)
	if err != nil {
		return 0, fmt.Errorf("list retained audios: %w", err)
	}
	rewritten := 0
	for _, r := range refs {
		if !filepath.IsAbs(r.Path) {
			continue // already relative — nothing to do
		}
		// Stored path is absolute (legacy). Only rewrite if it's INSIDE
		// the current audioDir; rows pointing elsewhere stay legacy.
		rel, err := filepath.Rel(dirAbs, r.Path)
		if err != nil {
			continue
		}
		if rel == "." || rel == "" || (len(rel) >= 2 && rel[:2] == "..") {
			continue
		}
		if err := store.SetAudioPath(ctx, r.ID, rel); err != nil {
			return rewritten, fmt.Errorf("set audio_path id=%d: %w", r.ID, err)
		}
		rewritten++
	}
	return rewritten, nil
}

// CheckDirPerm warns at Warn level if audioDir EXISTS with a mode
// wider than 0o700. SaveOriginal's MkdirAll(0o700) only sets perms on
// freshly-created directories — an operator who pre-created
// `/data/audio` with 0o755 (or who restored a backup with relaxed
// permissions) would get audio files inheriting their umask under a
// world-traversable parent, silently weakening the
// owner-read+write-only contract documented in SECURITY-MODEL.
//
// Read-only: never chmods. Changing perms behind the operator's back
// could be just as surprising as leaving them lax. The Warn is the
// signal — the operator decides whether to tighten the directory.
//
// No-op on Windows: os.FileMode bits from Stat are synthetic there,
// so the comparison would either always pass or always fail. We track
// that limit in `.agents/context/gotchas.md`.
func CheckDirPerm(audioDir string, logger *slog.Logger) {
	if audioDir == "" || runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(audioDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return // not created yet — MkdirAll will give it 0700 on first save
		}
		logger.Warn("audio retain: stat dir for perm check", "dir", audioDir, "err", err)
		return
	}
	if !info.IsDir() {
		logger.Warn("audio retain: AUDIO_DIR is not a directory", "path", audioDir)
		return
	}
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		logger.Warn("audio retain: AUDIO_DIR has permissions wider than 0700; retained audio will inherit your umask under a non-owner-only parent",
			"dir", audioDir, "mode", fmt.Sprintf("%#o", mode))
	}
}

// ScanOrphans walks audioDir for *.oga files and counts those that are
// NOT referenced by any notes.audio_path row. Each orphan is logged at
// Warn level (with path). Does not delete anything — operator decides.
//
// Common causes for orphans: DB was rebuilt from scratch but the audio
// dir was preserved; operator copied a backup of AUDIO_DIR into a new
// deploy; manual file drop. ScanOrphans is read-only and runs once at
// startup, after RelativizeLegacyPaths so the comparison happens
// against the post-normalize state.
//
// Returns the orphan count for the startup banner.
func ScanOrphans(ctx context.Context, store *db.DB, audioDir string, logger *slog.Logger) (int, error) {
	if audioDir == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(audioDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil // not created yet; not an error
		}
		return 0, fmt.Errorf("read audio dir: %w", err)
	}
	dirAbs, err := filepath.Abs(audioDir)
	if err != nil {
		return 0, fmt.Errorf("abs audio dir: %w", err)
	}

	// Build a set of basenames known to the DB, resolved against the
	// current dir so relative AND absolute-inside-dir rows both match.
	refs, err := store.AllRetainedAudios(ctx)
	if err != nil {
		return 0, fmt.Errorf("list retained audios: %w", err)
	}
	known := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		resolved := Resolve(dirAbs, r.Path)
		if pathInside(dirAbs, resolved) {
			known[filepath.Base(resolved)] = struct{}{}
		}
	}

	orphans := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".oga" {
			continue
		}
		if _, ok := known[name]; ok {
			continue
		}
		logger.Warn("audio retain: orphan file (no matching notes.audio_path)",
			"file", filepath.Join(dirAbs, name))
		orphans++
	}
	return orphans, nil
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
		// Stored path may be relative (new) or absolute (legacy). Resolve
		// against the active dir before any filesystem touch.
		resolved := Resolve(dirAbs, r.Path)
		if !pathInside(dirAbs, resolved) {
			// Defensive: drop the DB pointer; do NOT touch the file.
			// Legacy absolute paths under a previous AUDIO_DIR land here.
			logger.Warn("audio janitor: skipping path outside managed dir",
				"id", r.ID, "stored", r.Path, "resolved", resolved)
			_ = store.ClearAudioPath(ctx, r.ID)
			continue
		}
		if err := os.Remove(resolved); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Warn("audio janitor: remove failed", "id", r.ID, "path", resolved, "err", err)
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
