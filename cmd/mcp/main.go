package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"voicelog/internal/db"
	"voicelog/internal/mcp"
	"voicelog/internal/whisper"
	"voicelog/migrations"
)

const minTokenLen = 16

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	dbPath := mustEnv(logger, "DB_PATH")
	token := mustEnv(logger, "MCP_TOKEN")
	if len(token) < minTokenLen {
		logger.Error("MCP_TOKEN too short", "len", len(token), "min", minTokenLen)
		os.Exit(1)
	}

	port := os.Getenv("MCP_PORT")
	if port == "" {
		port = "8081"
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

	// Optional: wire whisper so the retranscribe MCP tool can re-run
	// transcription on retained audio. If WHISPER_URL is unset, the tool
	// is still registered but returns a clear "unavailable" error.
	var deps mcp.RetranscribeDeps
	if whisperURL := os.Getenv("WHISPER_URL"); whisperURL != "" {
		deps.Whisper = whisper.New(whisperURL)
		deps.BasePrompt = os.Getenv("WHISPER_PROMPT")
		deps.HallucinationThresh = 0.6 // matches the bot's default
		if v := os.Getenv("HALLUCINATION_THRESHOLD"); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 || f > 1 {
				logger.Error("HALLUCINATION_THRESHOLD must be a float in [0, 1]", "value", v)
				os.Exit(1)
			}
			deps.HallucinationThresh = f
		}
	}

	mcpServer := mcp.NewServer(store, deps, logger)

	mcpHTTP := server.NewStreamableHTTPServer(mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithStateLess(true),
	)

	addr := ":" + port
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           bearerAuth(token, mcpHTTP),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// DB maintenance (WAL checkpoint + monthly VACUUM). Runs only here in
	// the mcp container so two processes don't race on PRAGMA. Disable
	// via env if the operator wants to manage maintenance externally.
	if os.Getenv("DB_MAINTENANCE_DISABLED") == "" {
		go db.MaintenanceLoop(ctx, store, logger)
	}

	logger.Info("voicelog mcp listening",
		"addr", addr, "endpoint", "/mcp", "db", dbPath, "auth", "bearer",
		"retranscribe", deps.Whisper != nil,
		"maintenance", os.Getenv("DB_MAINTENANCE_DISABLED") == "")

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("shutting down", "signal", sig.String())
	case err := <-errCh:
		logger.Error("http server crashed", "err", err)
		os.Exit(1)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", "err", err)
	}
}

// bearerAuth requires every request to carry "Authorization: Bearer <token>".
// Constant-time comparison defeats timing oracles on token prefixes.
func bearerAuth(token string, next http.Handler) http.Handler {
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, expected) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="voicelog"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mustEnv(logger *slog.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		logger.Error("missing env var", "key", key)
		os.Exit(1)
	}
	return v
}
