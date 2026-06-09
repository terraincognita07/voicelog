// Package diag wires runtime diagnostics: opt-in pprof endpoint that lets
// an operator capture heap / goroutine / cpu snapshots without rebuilding
// the binary or restarting in a debug mode. Off by default; gated by the
// PPROF_ADDR env var so production deploys stay quiet.
package diag

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// StartPprof binds a dedicated HTTP listener exposing the net/http/pprof
// handlers on addr. Returns a stopper that drains the listener with a 5s
// timeout. When addr is empty the returned stopper is a no-op — that's
// the default "off" state.
//
// addr MUST be a loopback bind target — 127.0.0.1, [::1], or the literal
// "localhost" (matched without a DNS lookup; see isLoopback). Anything
// else is refused; the operator is expected to use an SSH tunnel for
// remote profiling. pprof endpoints expose enough
// internal state (goroutine stacks, allocation sites, source lines on
// /debug/pprof/symbol) that we treat them as effectively read-only RCE
// surface and refuse to expose them broadly by accident.
//
// pprof handlers register on a dedicated ServeMux so they don't pollute
// http.DefaultServeMux that other packages (or future additions) may
// rely on.
func StartPprof(addr string, logger *slog.Logger) (stop func(context.Context), err error) {
	if addr == "" {
		return func(context.Context) {}, nil
	}
	if !isLoopback(addr) {
		return nil, fmt.Errorf("PPROF_ADDR must bind a loopback interface (got %q); use an SSH tunnel for remote profiling", addr)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("pprof listen: %w", err)
	}

	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("pprof server", "err", serveErr)
		}
	}()
	logger.Info("pprof listening", "addr", ln.Addr().String())

	return func(parent context.Context) {
		shutdownCtx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}, nil
}

// isLoopback returns true iff addr's host portion is a loopback. The
// literal "localhost" passes, as does any IP that net.IP.IsLoopback
// accepts — i.e. the whole 127.0.0.0/8 block plus ::1. Everything else
// (bare ":N", "0.0.0.0:N", real hostnames, public IPs) is refused. We
// don't run a DNS lookup — operators that need a custom loopback
// hostname can patch the binary; the common case stays safe.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch host {
	case "":
		return false // bare ":6060" binds every interface — refuse
	case "localhost":
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
