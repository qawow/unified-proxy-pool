package settings

import (
	"testing"
	"time"

	"unified-proxy-pool/internal/models"
)

func TestValidateSettings(t *testing.T) {
	valid := models.Settings{
		PanelHost:                      "0.0.0.0",
		PanelPort:                      7890,
		SpeedTestEnabled:               false,
		LatencyTestURL:                 "https://cp.cloudflare.com/generate_204",
		SpeedTestURL:                   "https://speed.cloudflare.com/__down?bytes=5000000",
		LatencyTimeoutMS:               5000,
		SpeedTimeoutMS:                 10000,
		LatencyConcurrency:             32,
		SpeedConcurrency:               1,
		DefaultSubscriptionIntervalSec: 3600,
		MihomoControllerSecret:         "secret-token",
		FailureRetryCount:              2,
		LogLevel:                       "info",
		SpeedMaxBytes:                  5000000,
		CreatedAt:                      time.Now(),
		UpdatedAt:                      time.Now(),
	}
	if err := validateSettings(valid); err != nil {
		t.Fatalf("validateSettings(valid) error = %v", err)
	}

	invalid := valid
	invalid.PanelHost = ""
	if err := validateSettings(invalid); err == nil {
		t.Fatalf("expected panel host validation error")
	}

	invalid = valid
	invalid.LogLevel = "verbose"
	if err := validateSettings(invalid); err == nil {
		t.Fatalf("expected log level validation error")
	}
	invalid = valid
	invalid.SpeedConcurrency = 5
	if err := validateSettings(invalid); err == nil {
		t.Fatalf("expected speed concurrency upper-bound validation error")
	}
}
