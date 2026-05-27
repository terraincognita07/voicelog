// Package config holds small helpers for loading env-driven config at
// startup. Each helper logs and exits(1) on invalid input — the
// philosophy here is fail-fast at boot rather than guessing through
// the bug later. Inner unexported helpers return errors so unit tests
// don't have to fork sub-processes to drive the exit path.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

// MustEnv returns os.Getenv(key) or exits with code 1 if it's empty.
// Used for required configuration where a missing value is unambiguously
// a deploy error (BOT_TOKEN, DB_PATH, etc).
func MustEnv(logger *slog.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		logger.Error("missing env var", "key", key)
		os.Exit(1)
	}
	return v
}

// ParseFloat01 returns env[key] parsed as a float in [0, 1], or
// fallback if env[key] is unset. Logs and exits(1) on syntactically
// invalid or out-of-range values. Used for HALLUCINATION_THRESHOLD.
func ParseFloat01(logger *slog.Logger, key string, fallback float64) float64 {
	value := os.Getenv(key)
	f, present, err := parseFloat01(value)
	if err != nil {
		logger.Error(key+" must be a float in [0, 1]", "value", value, "err", err)
		os.Exit(1)
	}
	if !present {
		return fallback
	}
	return f
}

// parseFloat01 is the testable core of ParseFloat01. Returns:
//   - (f, true, nil)  — valid value parsed
//   - (0, false, nil) — input was empty (caller should use fallback)
//   - (0, true, err)  — invalid (parse fail or out of range)
func parseFloat01(value string) (float64, bool, error) {
	if value == "" {
		return 0, false, nil
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, true, fmt.Errorf("not a float: %q", value)
	}
	if f < 0 || f > 1 {
		return 0, true, fmt.Errorf("%v out of [0, 1]", f)
	}
	return f, true, nil
}
