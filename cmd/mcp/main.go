package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"voicelog/internal/db"
	"voicelog/internal/mcp"
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

	mcpServer := mcp.NewServer(store, logger)

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

	logger.Info("voicelog mcp listening",
		"addr", addr, "endpoint", "/mcp", "db", dbPath, "auth", "bearer")

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
