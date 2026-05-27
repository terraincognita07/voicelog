package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"voicelog/internal/audio"
	"voicelog/internal/db"
	"voicelog/internal/telegram"
	"voicelog/internal/whisper"
	"voicelog/migrations"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	token := mustEnv(logger, "BOT_TOKEN")
	allowedStr := mustEnv(logger, "ALLOWED_USER_ID")
	dbPath := mustEnv(logger, "DB_PATH")
	whisperURL := mustEnv(logger, "WHISPER_URL")

	allowedUser, err := strconv.ParseInt(allowedStr, 10, 64)
	if err != nil {
		logger.Error("ALLOWED_USER_ID must be int64", "value", allowedStr, "err", err)
		os.Exit(1)
	}

	locale := os.Getenv("BOT_LOCALE")         // empty falls back to "en" inside telegram.New
	basePrompt := os.Getenv("WHISPER_PROMPT") // optional whisper "initial prompt"

	// Audio retention. Default OFF: AUDIO_RETENTION_DAYS=0 (or unset)
	// keeps the old "delete tmp WAV after transcription, nothing
	// persisted" behavior. Any positive integer enables retention.
	audioRetentionDays := 0
	if v := os.Getenv("AUDIO_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			logger.Error("AUDIO_RETENTION_DAYS must be a non-negative integer", "value", v)
			os.Exit(1)
		}
		audioRetentionDays = n
	}
	audioDir := os.Getenv("AUDIO_DIR")
	if audioDir == "" {
		audioDir = "/data/audio"
	}
	if audioRetentionDays == 0 {
		audioDir = "" // disables retention paths inside the bot
	}

	hallucinationThresh := 0.0 // 0 → telegram.New picks default 0.6
	if v := os.Getenv("HALLUCINATION_THRESHOLD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 1 {
			logger.Error("HALLUCINATION_THRESHOLD must be a float in [0, 1]", "value", v)
			os.Exit(1)
		}
		hallucinationThresh = f
	}

	// Disk-full guard. 0 disables the check. Default 500 MB matches the
	// issue spec and gives generous headroom over typical DB+audio
	// growth for a personal journal.
	minFreeDiskMB := uint64(500)
	if v := os.Getenv("MIN_FREE_DISK_MB"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			logger.Error("MIN_FREE_DISK_MB must be a non-negative integer", "value", v)
			os.Exit(1)
		}
		minFreeDiskMB = n
	}
	// DataDir = parent of DB file. The bot writes its DB AND its
	// retained-audio dir into the same /data mount, so monitoring the
	// db parent is sufficient.
	dataDir := filepath.Dir(dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := db.Open(ctx, dbPath)
	if err != nil {
		logger.Error("open db", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.Migrate(ctx, migrations.FS); err != nil {
		logger.Error("migrate", "err", err)
		os.Exit(1)
	}

	// Audio retention housekeeping. Only meaningful when audioDir is
	// set (retention enabled). Both passes are read-mostly and safe to
	// run on every startup.
	if audioDir != "" {
		if n, err := audio.RelativizeLegacyPaths(ctx, store, audioDir); err != nil {
			logger.Warn("audio retain: relativize legacy paths failed", "err", err)
		} else if n > 0 {
			logger.Info("audio retain: relativized legacy abs paths", "count", n)
		}
		if n, err := audio.ScanOrphans(ctx, store, audioDir, logger); err != nil {
			logger.Warn("audio retain: orphan scan failed", "err", err)
		} else if n > 0 {
			logger.Warn("audio retain: orphan files found in AUDIO_DIR (not deleted; review manually)", "count", n)
		}
	}

	w := whisper.New(whisperURL)

	bot, err := telegram.New(token, telegram.Config{
		Locale:              locale,
		BasePrompt:          basePrompt,
		AudioDir:            audioDir,
		HallucinationThresh: hallucinationThresh,
		DataDir:             dataDir,
		MinFreeDiskMB:       minFreeDiskMB,
	}, allowedUser, store, w, logger)
	if err != nil {
		logger.Error("init bot", "err", err)
		os.Exit(1)
	}

	go bot.Start()

	// Audio janitor runs only when retention is enabled. Cancellation is
	// tied to ctx (the same Background context the bot inherited above);
	// on SIGTERM the deferred cancel() unblocks the janitor's select.
	if audioRetentionDays > 0 {
		go audio.Janitor(ctx, store, audioDir, audioRetentionDays, logger)
	}

	logger.Info("voicelog bot started",
		"allowed_user", allowedUser,
		"db", dbPath,
		"whisper_url", whisperURL,
		"locale", locale,
		"base_prompt_len", len(basePrompt),
		"audio_retention_days", audioRetentionDays,
		"audio_dir", audioDir,
		"min_free_disk_mb", minFreeDiskMB)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	sig := <-sigCh
	logger.Info("shutting down", "signal", sig.String())
	bot.Stop()
}

func mustEnv(logger *slog.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		logger.Error("missing env var", "key", key)
		os.Exit(1)
	}
	return v
}
