package diag

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestIsLoopback_Accept(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1:6060",
		"127.0.0.1:0",
		"[::1]:6060",
		"localhost:6060",
	} {
		if !isLoopback(addr) {
			t.Errorf("isLoopback(%q) = false, want true", addr)
		}
	}
}

func TestIsLoopback_Refuse(t *testing.T) {
	for _, addr := range []string{
		":6060",            // bare port — binds all interfaces
		"0.0.0.0:6060",     // explicit all-interfaces
		"8.8.8.8:6060",     // public IP
		"example.com:6060", // hostname that isn't 'localhost'
		"garbage",          // not a valid host:port
		"",                 // empty (caller short-circuits, but defensively)
	} {
		if isLoopback(addr) {
			t.Errorf("isLoopback(%q) = true, want false", addr)
		}
	}
}

// TestIsLoopback_Accepts127Range documents that any 127.0.0.0/8 address
// is loopback per IANA / Go's net.IP.IsLoopback. Not a "refuse" case —
// an operator who knowingly picks 127.0.0.2 is still binding loopback.
func TestIsLoopback_Accepts127Range(t *testing.T) {
	for _, addr := range []string{"127.0.0.2:6060", "127.5.5.5:6060", "127.255.255.255:6060"} {
		if !isLoopback(addr) {
			t.Errorf("isLoopback(%q) = false, want true (127.0.0.0/8 is loopback)", addr)
		}
	}
}

// silentLogger is shared across tests so the pprof server's log lines
// don't drown the test output.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestStartPprof_DisabledOnEmptyAddr(t *testing.T) {
	stop, err := StartPprof("", silentLogger())
	if err != nil {
		t.Fatalf("empty addr should not error: %v", err)
	}
	if stop == nil {
		t.Fatal("stop should always be non-nil")
	}
	stop(context.Background()) // must be safe to call
}

func TestStartPprof_RefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{":6060", "0.0.0.0:6060", "8.8.8.8:6060"} {
		stop, err := StartPprof(addr, silentLogger())
		if err == nil {
			if stop != nil {
				stop(context.Background())
			}
			t.Errorf("StartPprof(%q): want error, got nil", addr)
			continue
		}
		if !strings.Contains(err.Error(), "loopback") {
			t.Errorf("StartPprof(%q) err = %v; should mention 'loopback'", addr, err)
		}
	}
}

func TestStartPprof_StopIsIdempotentAndFast(t *testing.T) {
	// Pick a known loopback port (use 0 → random). Then call stop twice
	// to confirm idempotency and that it returns within the timeout.
	stop, err := StartPprof("127.0.0.1:0", silentLogger())
	if err != nil {
		t.Fatalf("StartPprof: %v", err)
	}
	start := time.Now()
	stop(context.Background())
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Errorf("first stop took %v, expected <6s", elapsed)
	}
	// Second stop must not panic. We rely on http.Server.Shutdown being
	// idempotent (returns ErrServerClosed on subsequent calls), which
	// our stopper swallows.
	stop(context.Background())
}

// TestStartPprof_RoutesRespond is the meaningful happy-path test.
// Instead of skipping, we bind on an explicit known port that's
// unlikely to clash with anything else on a dev machine. If the
// bind fails the test skips (CI may have port conflicts).
func TestStartPprof_RoutesRespond(t *testing.T) {
	const addr = "127.0.0.1:16060"
	stop, err := StartPprof(addr, silentLogger())
	if err != nil {
		t.Skipf("could not bind %s (probably in use): %v", addr, err)
	}
	defer stop(context.Background())

	// Give the listener a moment to accept connections.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/debug/pprof/")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", resp.StatusCode)
				return
			}
			if !strings.Contains(string(body), "pprof") {
				t.Errorf("body should reference pprof; got first 200 bytes: %q", string(body)[:min(200, len(body))])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pprof listener never became reachable on %s", addr)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
