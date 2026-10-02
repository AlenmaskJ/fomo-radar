package config

import (
	"testing"
	"time"
)

// This fails if Load stops supplying the scanner's safe local defaults.
func TestLoadDefaults(t *testing.T) {
	t.Setenv("FOMO_DB", "")
	t.Setenv("FOMO_HTTP_ADDR", "")
	t.Setenv("FOMO_DEEP_SCAN_THRESHOLD", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBPath != "./fomo.db" {
		t.Fatalf("DBPath=%q", cfg.DBPath)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("HTTPAddr=%q", cfg.HTTPAddr)
	}
	if cfg.DeepScanThreshold != 40 {
		t.Fatalf("threshold=%d", cfg.DeepScanThreshold)
	}
	if cfg.CandidateMaxAge != 6*time.Hour {
		t.Fatalf("CandidateMaxAge=%s", cfg.CandidateMaxAge)
	}
	if cfg.EarlyMaxAge != 2*time.Hour {
		t.Fatalf("EarlyMaxAge=%s", cfg.EarlyMaxAge)
	}
	if cfg.PreferredMaxMCUSD != 500000 {
		t.Fatalf("PreferredMaxMCUSD=%f", cfg.PreferredMaxMCUSD)
	}
	if cfg.HTTPTimeout != 8*time.Second {
		t.Fatalf("HTTPTimeout=%s", cfg.HTTPTimeout)
	}
}

// This fails if an out-of-range score threshold is accepted.
func TestLoadRejectsInvalidThreshold(t *testing.T) {
	t.Setenv("FOMO_DEEP_SCAN_THRESHOLD", "101")
	if _, err := Load(); err == nil {
		t.Fatal("expected validation error")
	}
}
