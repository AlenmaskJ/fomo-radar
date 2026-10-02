package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	defaultDBPath            = "./fomo.db"
	defaultHTTPAddr          = "127.0.0.1:8080"
	defaultDeepScanThreshold = 40
)

// Config holds the process-wide scanner settings.
type Config struct {
	DBPath            string
	HTTPAddr          string
	DeepScanThreshold int
	CandidateMaxAge   time.Duration
	EarlyMaxAge       time.Duration
	PreferredMaxMCUSD float64
	HTTPTimeout       time.Duration
}

// Load reads the supported environment overrides and validates the result.
func Load() (Config, error) {
	threshold := defaultDeepScanThreshold
	if raw := os.Getenv("FOMO_DEEP_SCAN_THRESHOLD"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse FOMO_DEEP_SCAN_THRESHOLD: %w", err)
		}
		threshold = parsed
	}
	if threshold < 0 || threshold > 100 {
		return Config{}, fmt.Errorf("FOMO_DEEP_SCAN_THRESHOLD must be between 0 and 100")
	}

	return Config{
		DBPath:            envOrDefault("FOMO_DB", defaultDBPath),
		HTTPAddr:          envOrDefault("FOMO_HTTP_ADDR", defaultHTTPAddr),
		DeepScanThreshold: threshold,
		CandidateMaxAge:   6 * time.Hour,
		EarlyMaxAge:       2 * time.Hour,
		PreferredMaxMCUSD: 500000,
		HTTPTimeout:       8 * time.Second,
	}, nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
