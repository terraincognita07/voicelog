package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/terraincognita07/voicelog/internal/config"
	"github.com/terraincognita07/voicelog/internal/db"
	"github.com/terraincognita07/voicelog/internal/db/migrations"
	"github.com/terraincognita07/voicelog/internal/diag"
	"github.com/terraincognita07/voicelog/internal/mcp"
	"github.com/terraincognita07/voicelog/internal/whisper"
)

const minTokenLen = 16

// maxRequestBytes caps an incoming MCP request body. Tool calls are small
// JSON-RPC messages; 1 MiB leaves ample room (delete_notes with ~100k ids)
// while preventing an unbounded io.ReadAll from exhausting memory.
const maxRequestBytes = 1 << 20

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	dbPath := config.MustEnv(logger, "DB_PATH")
	token := config.MustEnv(logger, "MCP_TOKEN")
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

	// Optional pprof. Empty PPROF_ADDR = disabled (default). Refuses any
	// non-loopback bind — use an SSH tunnel for remote profiling.
	pprofStop, err := diag.StartPprof(os.Getenv("PPROF_ADDR"), logger)
	if err != nil {
		logger.Error("pprof", "err", err)
		os.Exit(1)
	}
	defer pprofStop(context.Background())

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

	// audio_path is stored relative to AUDIO_DIR for new notes. Set
	// unconditionally: delete_notes must remove retained audio whether or
	// not whisper is wired, otherwise whisper-less mcp silently leaks files
	// (and breaks the documented delete contract). Same default as cmd/bot
	// so the two containers agree out of the box; operators who change
	// AUDIO_DIR must set it in both.
	var deps mcp.RetranscribeDeps
	deps.AudioDir = os.Getenv("AUDIO_DIR")
	if deps.AudioDir == "" {
		deps.AudioDir = "/data/audio"
	}

	// Optional: wire whisper so the retranscribe MCP tool can re-run
	// transcription on retained audio. If WHISPER_URL is unset, the tool
	// is still registered but returns a clear "unavailable" error.
	if whisperURL := os.Getenv("WHISPER_URL"); whisperURL != "" {
		deps.Whisper = whisper.New(whisperURL)
		deps.Whisper.Logger = logger // enables one-time "no segments" warning
		deps.BasePrompt = os.Getenv("WHISPER_PROMPT")
		deps.HallucinationThresh = config.ParseFloat01(logger, "HALLUCINATION_THRESHOLD", 0.6)
	}

	mcpServer := mcp.NewServer(store, deps, logger)

	mcpHTTP := server.NewStreamableHTTPServer(mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithStateLess(true),
	)

	addr := ":" + port
	httpSrv := &http.Server{
		Addr: addr,
		// Cap the request body so a holder of a valid token can't OOM the
		// process with one giant POST (mcp-go slurps the body via io.ReadAll).
		// MaxBytesHandler swaps r.Body for a MaxBytesReader per request; MCP
		// tool calls are small JSON, 1 MiB is generous.
		Handler:           http.MaxBytesHandler(mcp.BearerAuth(token, mcpHTTP), maxRequestBytes),
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout bounds a slow-loris body; IdleTimeout reaps parked
		// keep-alive connections so an unauthenticated client can't hoard fds.
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second,
		// Deliberately no WriteTimeout: retranscribe streams a response that
		// can take minutes (a whisper re-run). A wall-clock write cap would
		// truncate it — the per-tool context timeouts bound handlers instead.
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
