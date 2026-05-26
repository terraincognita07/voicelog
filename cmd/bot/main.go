package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

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

	w := whisper.New(whisperURL)

	bot, err := telegram.New(token, allowedUser, store, w, logger)
	if err != nil {
		logger.Error("init bot", "err", err)
		os.Exit(1)
	}

	go bot.Start()
	logger.Info("voicelog bot started",
		"allowed_user", allowedUser,
		"db", dbPath,
		"whisper_url", whisperURL)

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
