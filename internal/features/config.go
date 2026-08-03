package features

import (
	"encoding/json"
	"strings"
)

// Config holds F1–F6 panel/runtime feature flags persisted as settings.feature_json.
type Config struct {
	// F1 dashboard cards: missing key = visible
	DashboardCards map[string]bool `json:"dashboard_cards,omitempty"`

	// F3
	DirectStickyEnabled bool  `json:"direct_sticky_enabled"`
	StickyTTLSec        int   `json:"sticky_ttl_sec"`
	RateLimitBytesPerSec int64 `json:"rate_limit_bytes_per_sec"`

	// F4
	FreeValidateURLs       []string `json:"free_validate_urls,omitempty"`
	SourceAutoDisableRate  float64  `json:"source_auto_disable_rate"` // 0-1, 0=off
	SourceMinSamples       int      `json:"source_min_samples"`

	// F5
	DirectAuthRequired bool     `json:"direct_auth_required"`
	AllowedCIDRs       []string `json:"allowed_cidrs,omitempty"`

	// F6
	WebhookURL          string   `json:"webhook_url,omitempty"`
	WebhookEvents       []string `json:"webhook_events,omitempty"`
	AlertValidatedMin   int      `json:"alert_validated_min"`
	TrafficSampleSec    int      `json:"traffic_sample_sec"`
	TrafficRetainHours  int      `json:"traffic_retain_hours"`

	// Chain proxy detailed options
	Chain ChainConfig `json:"chain,omitempty"`
}

// ChainConfig is the panel-editable multi-hop policy.
type ChainConfig struct {
	Enabled               bool     `json:"enabled"`
	ListenAddr            string   `json:"listen_addr,omitempty"`
	Hops                  int      `json:"hops"`
	FailoverTries         int      `json:"failover_tries"`
	DialTimeoutMS         int      `json:"dial_timeout_ms"`
	HopTimeoutMS          int      `json:"hop_timeout_ms"`
	PreferDistinctHost    bool     `json:"prefer_distinct_host"`
	PreferDistinctRegion  bool     `json:"prefer_distinct_region"`
	EntryProto            string   `json:"entry_proto,omitempty"`
	ExitProto             string   `json:"exit_proto,omitempty"`
	EntryRegion           string   `json:"entry_region,omitempty"`
	ExitRegion            string   `json:"exit_region,omitempty"`
	StickyEnabled         bool     `json:"sticky_enabled"`
	StickyTTLSec          int      `json:"sticky_ttl_sec"`
	AuthRequired          bool     `json:"auth_required"`
	Username              string   `json:"username,omitempty"`
	Password              string   `json:"password,omitempty"`
	AllowedCIDRs          []string `json:"allowed_cidrs,omitempty"`
	RateLimitBPS          int64    `json:"rate_limit_bps"`
	MaxParallelDial       int      `json:"max_parallel_dial"`
}

func DefaultChain() ChainConfig {
	return ChainConfig{
		Enabled:              true,
		ListenAddr:           "0.0.0.0:7893",
		Hops:                 2,
		FailoverTries:        6,
		DialTimeoutMS:        8000,
		HopTimeoutMS:         5000,
		PreferDistinctHost:   true,
		PreferDistinctRegion: false,
		StickyTTLSec:         600,
		MaxParallelDial:      1,
	}
}

func Default() Config {
	return Config{
		DashboardCards:        DefaultCards(),
		StickyTTLSec:          600,
		SourceAutoDisableRate: 0.15,
		SourceMinSamples:      20,
		AlertValidatedMin:     5,
		TrafficSampleSec:      60,
		TrafficRetainHours:    48,
		WebhookEvents:         []string{"validated_low", "validate_all_fail"},
		Chain:                 DefaultChain(),
	}
}

func DefaultCards() map[string]bool {
	keys := []string{
		"available", "health", "live_conn", "up_bytes", "down_bytes",
		"sources", "avg_score", "total", "single_hop", "chain", "lan", "events", "regions",
	}
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func Parse(raw string) Config {
	cfg := Default()
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return cfg
	}
	_ = json.Unmarshal([]byte(raw), &cfg)
	if cfg.DashboardCards == nil {
		cfg.DashboardCards = DefaultCards()
	} else {
		// merge defaults for missing keys
		for k, v := range DefaultCards() {
			if _, ok := cfg.DashboardCards[k]; !ok {
				cfg.DashboardCards[k] = v
			}
		}
	}
	if cfg.StickyTTLSec <= 0 {
		cfg.StickyTTLSec = 600
	}
	if cfg.TrafficSampleSec <= 0 {
		cfg.TrafficSampleSec = 60
	}
	if cfg.TrafficRetainHours <= 0 {
		cfg.TrafficRetainHours = 48
	}
	if cfg.SourceMinSamples <= 0 {
		cfg.SourceMinSamples = 20
	}
	cfg.Chain = normalizeChain(cfg.Chain)
	return cfg
}

func normalizeChain(c ChainConfig) ChainConfig {
	d := DefaultChain()
	// zero-value Enabled=false is ambiguous; if hops unset treat as default enabled
	if c.Hops == 0 && c.ListenAddr == "" && c.FailoverTries == 0 && c.DialTimeoutMS == 0 {
		return d
	}
	if c.Hops < 2 {
		c.Hops = d.Hops
	}
	if c.Hops > 4 {
		c.Hops = 4
	}
	if c.FailoverTries <= 0 {
		c.FailoverTries = d.FailoverTries
	}
	if c.DialTimeoutMS <= 0 {
		c.DialTimeoutMS = d.DialTimeoutMS
	}
	if c.HopTimeoutMS <= 0 {
		c.HopTimeoutMS = d.HopTimeoutMS
	}
	if c.ListenAddr == "" {
		c.ListenAddr = d.ListenAddr
	}
	if c.StickyTTLSec <= 0 {
		c.StickyTTLSec = d.StickyTTLSec
	}
	if c.MaxParallelDial < 1 {
		c.MaxParallelDial = 1
	}
	if c.MaxParallelDial > 3 {
		c.MaxParallelDial = 3
	}
	// PreferDistinctHost default true when not explicitly in JSON is hard;
	// keep whatever was unmarshaled (false is valid).
	return c
}

func (c Config) Marshal() string {
	b, err := json.Marshal(c)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func (c Config) CardVisible(key string) bool {
	if c.DashboardCards == nil {
		return true
	}
	v, ok := c.DashboardCards[key]
	if !ok {
		return true
	}
	return v
}

func (c Config) ValidateURLs(fallback string) []string {
	var out []string
	for _, u := range c.FreeValidateURLs {
		u = strings.TrimSpace(u)
		if u != "" {
			out = append(out, u)
		}
	}
	if len(out) == 0 && strings.TrimSpace(fallback) != "" {
		out = []string{strings.TrimSpace(fallback)}
	}
	return out
}
