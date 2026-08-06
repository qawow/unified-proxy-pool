package settings

import (
	"testing"
	"time"

	"unified-proxy-pool/internal/models"
)

// validFixture mirrors what fillDefaults guarantees before validateSettings
// runs, so every field validateSettings inspects must be populated here. When a
// new rule is added to validateSettings, this fixture has to grow with it.
func validFixture() models.Settings {
	return models.Settings{
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
		SessionMaxAgeSec:               7 * 24 * 3600,
		ScrapeIntervalSec:              300,
		ValidateIntervalSec:            120,
		FreeValidateURL:                "https://www.gstatic.com/generate_204",
		FreeValidateTimeoutMS:          8000,
		FreeValidateConcurrency:        32,
		ProxyChainHops:                 2,
		CreatedAt:                      time.Now(),
		UpdatedAt:                      time.Now(),
	}
}

func TestValidateSettingsAcceptsDefaults(t *testing.T) {
	if err := validateSettings(validFixture()); err != nil {
		t.Fatalf("validateSettings(valid) error = %v", err)
	}
}

// Each case is a subtest so a stale fixture cannot abort the run and silently
// skip the remaining assertions.
func TestValidateSettingsRejectsBadValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*models.Settings)
	}{
		{"empty panel host", func(s *models.Settings) { s.PanelHost = "" }},
		{"port out of range", func(s *models.Settings) { s.PanelPort = 70000 }},
		{"unknown log level", func(s *models.Settings) { s.LogLevel = "verbose" }},
		{"speed concurrency above cap", func(s *models.Settings) { s.SpeedConcurrency = 5 }},
		{"session max age below 1h", func(s *models.Settings) { s.SessionMaxAgeSec = 60 }},
		{"scrape interval too small", func(s *models.Settings) { s.ScrapeIntervalSec = 30 }},
		{"validate interval too small", func(s *models.Settings) { s.ValidateIntervalSec = 10 }},
		{"empty free validate url", func(s *models.Settings) { s.FreeValidateURL = "" }},
		{"free validate timeout too small", func(s *models.Settings) { s.FreeValidateTimeoutMS = 500 }},
		{"free validate concurrency too high", func(s *models.Settings) { s.FreeValidateConcurrency = 300 }},
		{"chain hops below 2", func(s *models.Settings) { s.ProxyChainHops = 1 }},
		{"chain hops above 4", func(s *models.Settings) { s.ProxyChainHops = 5 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := validFixture()
			tc.mutate(&item)
			if err := validateSettings(item); err == nil {
				t.Errorf("expected a validation error for %s", tc.name)
			}
		})
	}
}
