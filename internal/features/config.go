package features

import (
	"encoding/json"
	"strings"
	"time"

	"unified-proxy-pool/internal/chanpolicy"
	"unified-proxy-pool/internal/geoip"
)

// Config holds F1–F6 panel/runtime feature flags persisted as settings.feature_json.
type Config struct {
	// F1 dashboard cards: missing key = visible
	DashboardCards map[string]bool `json:"dashboard_cards,omitempty"`

	// F3
	DirectStickyEnabled  bool  `json:"direct_sticky_enabled"`
	StickyTTLSec         int   `json:"sticky_ttl_sec"`
	RateLimitBytesPerSec int64 `json:"rate_limit_bytes_per_sec"`

	// F4
	FreeValidateURLs      []string `json:"free_validate_urls,omitempty"`
	SourceAutoDisableRate float64  `json:"source_auto_disable_rate"` // 0-1, 0=off
	SourceMinSamples      int      `json:"source_min_samples"`

	// F5
	DirectAuthRequired bool     `json:"direct_auth_required"`
	AllowedCIDRs       []string `json:"allowed_cidrs,omitempty"`
	// PublicOpen exposes /api/public to non-LAN clients. Default false: only
	// RFC1918/loopback plus AllowedCIDRs. LAN debug stays usable without login.
	PublicOpen bool `json:"public_open,omitempty"`

	// F6
	WebhookURL         string   `json:"webhook_url,omitempty"`
	WebhookEvents      []string `json:"webhook_events,omitempty"`
	AlertValidatedMin  int      `json:"alert_validated_min"`
	TrafficSampleSec   int      `json:"traffic_sample_sec"`
	TrafficRetainHours int      `json:"traffic_retain_hours"`

	// Chain proxy detailed options
	Chain ChainConfig `json:"chain,omitempty"`

	// Per-channel temporary bans + selection strategy
	Channels ChannelPolicyConfig `json:"channels,omitempty"`

	// ScrapeProxy is how crawlers reach GitHub/list hosts. Empty = env
	// HTTP(S)_PROXY. "none" disables that. "direct"/"7892" and "chain"/"7893"
	// go through this process's own exits. Otherwise an http:// or socks5:// URL.
	ScrapeProxy string `json:"scrape_proxy,omitempty"`

	// CountryFilterEnabled nil = default on. false turns the deny list off.
	CountryFilterEnabled *bool `json:"country_filter_enabled,omitempty"`
	// CheckExitCountry nil = default on: geolocate through the proxy, not the host.
	CheckExitCountry *bool `json:"check_exit_country,omitempty"`
	// BlockedCountries nil = default ["CN"]. Empty slice = block nothing.
	// HK/TW/MO are not included; add them here to drop those too.
	BlockedCountries []string `json:"blocked_countries,omitempty"`
}

// ChannelPolicyConfig is the panel-editable per-channel ban and selection policy.
// A "channel" is the destination a request is headed for, so a proxy the site has
// throttled can be sidelined for it alone.
type ChannelPolicyConfig struct {
	Enabled bool `json:"enabled"`
	// KeyMode: etld1 (fold subdomains) | host (exact) | off (no scoping)
	KeyMode   string `json:"key_mode,omitempty"`
	WindowSec int    `json:"window_sec"`

	// Ban rules. 0 disables an individual rule.
	ConsecutiveFails int     `json:"consecutive_fails"`
	FailRate         float64 `json:"fail_rate"`
	MinSamples       int     `json:"min_samples"`
	TimeoutFails     int     `json:"timeout_fails"`
	BanStatuses      []int   `json:"ban_statuses,omitempty"`

	BanTTLSec    int `json:"ban_ttl_sec"`
	BanTTLMaxSec int `json:"ban_ttl_max_sec"`

	MaxChannels       int `json:"max_channels"`
	MaxEntriesPerChan int `json:"max_entries_per_chan"`

	// Selection: weighted | random | rr | p2c
	PickStrategy string `json:"pick_strategy,omitempty"`
	CooldownSec  int    `json:"cooldown_sec"`

	LogRetainHours  int  `json:"log_retain_hours"`
	ReprobeOnExpiry bool `json:"reprobe_on_expiry"`
}

func DefaultChannels() ChannelPolicyConfig {
	return ChannelPolicyConfig{
		Enabled:           true,
		KeyMode:           "etld1",
		WindowSec:         300,
		ConsecutiveFails:  3,
		FailRate:          0.6,
		MinSamples:        5,
		TimeoutFails:      5,
		BanStatuses:       []int{403, 429},
		BanTTLSec:         60,
		BanTTLMaxSec:      1800,
		MaxChannels:       500,
		MaxEntriesPerChan: 2000,
		PickStrategy:      "weighted",
		CooldownSec:       30,
		LogRetainHours:    48,
		ReprobeOnExpiry:   true,
	}
}

// normalizeChannels fills gaps from the defaults. Like normalizeChain, a
// never-configured block is indistinguishable from an all-zero one, so an
// untouched config has to resolve to the shipped defaults rather than to
// "everything disabled".
func normalizeChannels(c ChannelPolicyConfig) ChannelPolicyConfig {
	d := DefaultChannels()
	if c.WindowSec == 0 && c.BanTTLSec == 0 && c.KeyMode == "" &&
		c.MaxChannels == 0 && c.PickStrategy == "" && len(c.BanStatuses) == 0 {
		return d
	}
	if c.KeyMode == "" {
		c.KeyMode = d.KeyMode
	}
	if c.WindowSec <= 0 {
		c.WindowSec = d.WindowSec
	}
	if c.MinSamples <= 0 {
		c.MinSamples = d.MinSamples
	}
	if c.BanTTLSec <= 0 {
		c.BanTTLSec = d.BanTTLSec
	}
	if c.BanTTLMaxSec < c.BanTTLSec {
		c.BanTTLMaxSec = d.BanTTLMaxSec
	}
	if c.MaxChannels <= 0 {
		c.MaxChannels = d.MaxChannels
	}
	if c.MaxEntriesPerChan <= 0 {
		c.MaxEntriesPerChan = d.MaxEntriesPerChan
	}
	if c.PickStrategy == "" {
		c.PickStrategy = d.PickStrategy
	}
	if c.CooldownSec < 0 {
		c.CooldownSec = 0
	}
	// Rule thresholds are left as given: 0 means "this rule off", which is a
	// legitimate choice, unlike a zero window or TTL.
	if c.FailRate < 0 || c.FailRate > 1 {
		c.FailRate = d.FailRate
	}
	return c
}

// ChainConfig is the panel-editable multi-hop policy.
type ChainConfig struct {
	Enabled              bool     `json:"enabled"`
	ListenAddr           string   `json:"listen_addr,omitempty"`
	Hops                 int      `json:"hops"`
	FailoverTries        int      `json:"failover_tries"`
	DialTimeoutMS        int      `json:"dial_timeout_ms"`
	HopTimeoutMS         int      `json:"hop_timeout_ms"`
	PreferDistinctHost   bool     `json:"prefer_distinct_host"`
	PreferDistinctRegion bool     `json:"prefer_distinct_region"`
	EntryProto           string   `json:"entry_proto,omitempty"`
	ExitProto            string   `json:"exit_proto,omitempty"`
	EntryRegion          string   `json:"entry_region,omitempty"`
	ExitRegion           string   `json:"exit_region,omitempty"`
	StickyEnabled        bool     `json:"sticky_enabled"`
	StickyTTLSec         int      `json:"sticky_ttl_sec"`
	AuthRequired         bool     `json:"auth_required"`
	Username             string   `json:"username,omitempty"`
	Password             string   `json:"password,omitempty"`
	AllowedCIDRs         []string `json:"allowed_cidrs,omitempty"`
	RateLimitBPS         int64    `json:"rate_limit_bps"`
	MaxParallelDial      int      `json:"max_parallel_dial"`
	ExitVia              string   `json:"exit_via,omitempty"`
	ExitViaMode          string   `json:"exit_via_mode,omitempty"`
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
	on, exitOn := true, true
	return Config{
		DashboardCards:        DefaultCards(),
		StickyTTLSec:          600,
		SourceAutoDisableRate: 0.15,
		SourceMinSamples:      20,
		AlertValidatedMin:     5,
		TrafficSampleSec:      60,
		TrafficRetainHours:    48,
		WebhookEvents:         []string{"validated_low", "validate_all_fail", "channel_ban"},
		Chain:                 DefaultChain(),
		Channels:              DefaultChannels(),
		CountryFilterEnabled:  &on,
		CheckExitCountry:      &exitOn,
		BlockedCountries:      []string{"CN"},
	}
}

func DefaultCards() map[string]bool {
	keys := []string{
		"available", "health", "live_conn", "up_bytes", "down_bytes",
		"sources", "avg_score", "total", "single_hop", "chain", "lan", "events", "regions",
		"channel_bans",
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
	cfg.Channels = normalizeChannels(cfg.Channels)
	cfg = normalizeCountry(cfg)
	return cfg
}

func normalizeCountry(cfg Config) Config {
	d := Default()
	if cfg.CountryFilterEnabled == nil {
		cfg.CountryFilterEnabled = d.CountryFilterEnabled
	}
	if cfg.CheckExitCountry == nil {
		cfg.CheckExitCountry = d.CheckExitCountry
	}
	if cfg.BlockedCountries == nil {
		cfg.BlockedCountries = append([]string{}, d.BlockedCountries...)
	}
	out := make([]string, 0, len(cfg.BlockedCountries))
	seen := map[string]struct{}{}
	for _, c := range cfg.BlockedCountries {
		n := geoip.Normalize(c)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	cfg.BlockedCountries = out
	return cfg
}

func (c Config) CountryEnabled() bool {
	return c.CountryFilterEnabled == nil || *c.CountryFilterEnabled
}

func (c Config) ExitCheckEnabled() bool {
	return c.CheckExitCountry == nil || *c.CheckExitCountry
}

func (c Config) CountryFilter() geoip.Filter {
	return geoip.BuildFilter(c.CountryEnabled(), c.ExitCheckEnabled(), c.BlockedCountries)
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

// ToPolicy converts the panel config into the runtime policy. The dependency
// runs this way (features -> chanpolicy) so both startup and the hot-apply path
// share one conversion instead of each mapping fields by hand.
func (c ChannelPolicyConfig) ToPolicy() chanpolicy.Policy {
	statuses := c.BanStatuses
	if statuses == nil {
		statuses = DefaultChannels().BanStatuses
	}
	return chanpolicy.Policy{
		Enabled:           c.Enabled,
		KeyMode:           c.KeyMode,
		WindowSec:         c.WindowSec,
		ConsecutiveFails:  c.ConsecutiveFails,
		FailRate:          c.FailRate,
		MinSamples:        c.MinSamples,
		TimeoutFails:      c.TimeoutFails,
		BanStatuses:       statuses,
		BanTTLSec:         c.BanTTLSec,
		BanTTLMaxSec:      c.BanTTLMaxSec,
		MaxChannels:       c.MaxChannels,
		MaxEntriesPerChan: c.MaxEntriesPerChan,
		PickStrategy:      c.PickStrategy,
		CooldownSec:       c.CooldownSec,
		LogRetainHours:    c.LogRetainHours,
		ReprobeOnExpiry:   c.ReprobeOnExpiry,
	}.Normalize()
}

// CooldownDuration is the recently-served suppression window for selection.
func (c ChannelPolicyConfig) CooldownDuration() time.Duration {
	if c.CooldownSec <= 0 {
		return 0
	}
	return time.Duration(c.CooldownSec) * time.Second
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
