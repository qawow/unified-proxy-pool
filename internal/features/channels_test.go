package features

import (
	"encoding/json"
	"testing"

	"unified-proxy-pool/internal/chanpolicy"
)

// An install that has never touched the channel settings has an all-zero block in
// feature_json. That must resolve to the shipped defaults, not to "every rule
// disabled and a zero-length ban" — the difference decides whether the feature
// silently does nothing on upgrade.
func TestParseEmptyFeatureJSONYieldsChannelDefaults(t *testing.T) {
	for _, raw := range []string{"", "{}", `{"webhook_url":"x"}`} {
		cfg := Parse(raw)
		got, want := cfg.Channels, DefaultChannels()
		if got.KeyMode != want.KeyMode {
			t.Errorf("Parse(%q): KeyMode = %q, want %q", raw, got.KeyMode, want.KeyMode)
		}
		if got.WindowSec != want.WindowSec {
			t.Errorf("Parse(%q): WindowSec = %d, want %d", raw, got.WindowSec, want.WindowSec)
		}
		if got.BanTTLSec != want.BanTTLSec {
			t.Errorf("Parse(%q): BanTTLSec = %d, want %d", raw, got.BanTTLSec, want.BanTTLSec)
		}
		if got.ConsecutiveFails != want.ConsecutiveFails {
			t.Errorf("Parse(%q): ConsecutiveFails = %d, want %d", raw, got.ConsecutiveFails, want.ConsecutiveFails)
		}
		if len(got.BanStatuses) == 0 {
			t.Errorf("Parse(%q): BanStatuses empty; 403/429 detection would never fire", raw)
		}
	}
}

// A partially-specified block keeps what the user set and fills the rest.
func TestParsePartialChannelConfigKeepsUserValues(t *testing.T) {
	cfg := Parse(`{"channels":{"enabled":true,"window_sec":120,"ban_ttl_sec":30}}`)
	c := cfg.Channels
	if c.WindowSec != 120 {
		t.Errorf("WindowSec = %d, want the supplied 120", c.WindowSec)
	}
	if c.BanTTLSec != 30 {
		t.Errorf("BanTTLSec = %d, want the supplied 30", c.BanTTLSec)
	}
	if c.KeyMode != DefaultChannels().KeyMode {
		t.Errorf("KeyMode = %q, want the default filled in", c.KeyMode)
	}
	if c.MaxChannels != DefaultChannels().MaxChannels {
		t.Errorf("MaxChannels = %d, want the default filled in", c.MaxChannels)
	}
}

// A rule threshold of 0 is a legitimate "turn this rule off" and must survive
// normalization, unlike a zero window or TTL.
func TestParseKeepsExplicitlyDisabledRules(t *testing.T) {
	cfg := Parse(`{"channels":{"enabled":true,"window_sec":300,"consecutive_fails":0,"timeout_fails":0,"fail_rate":0,"ban_ttl_sec":60}}`)
	c := cfg.Channels
	if c.ConsecutiveFails != 0 {
		t.Errorf("ConsecutiveFails = %d, want 0 preserved", c.ConsecutiveFails)
	}
	if c.TimeoutFails != 0 {
		t.Errorf("TimeoutFails = %d, want 0 preserved", c.TimeoutFails)
	}
	if c.FailRate != 0 {
		t.Errorf("FailRate = %v, want 0 preserved", c.FailRate)
	}
}

func TestChannelConfigRepairsOutOfRangeValues(t *testing.T) {
	cfg := Parse(`{"channels":{"enabled":true,"window_sec":-5,"fail_rate":9,"ban_ttl_sec":100,"ban_ttl_max_sec":10,"max_channels":-1,"cooldown_sec":-3,"pick_strategy":"bogus"}}`)
	c := cfg.Channels
	if c.WindowSec <= 0 {
		t.Errorf("WindowSec = %d, want a repaired positive value", c.WindowSec)
	}
	if c.FailRate < 0 || c.FailRate > 1 {
		t.Errorf("FailRate = %v, want it repaired into 0-1", c.FailRate)
	}
	if c.BanTTLMaxSec < c.BanTTLSec {
		t.Errorf("BanTTLMaxSec %d < BanTTLSec %d; a max below the base is unusable",
			c.BanTTLMaxSec, c.BanTTLSec)
	}
	if c.MaxChannels <= 0 {
		t.Errorf("MaxChannels = %d, want a repaired positive cap", c.MaxChannels)
	}
	if c.CooldownSec < 0 {
		t.Errorf("CooldownSec = %d, want it clamped to >= 0", c.CooldownSec)
	}
	// ToPolicy normalizes the strategy, since that is where it is consumed.
	if s := c.ToPolicy().PickStrategy; s != chanpolicy.StrategyWeighted {
		t.Errorf("PickStrategy = %q, want the weighted default for an unknown name", s)
	}
}

func TestToPolicyMapsEveryField(t *testing.T) {
	c := ChannelPolicyConfig{
		Enabled: true, KeyMode: "host", WindowSec: 240,
		ConsecutiveFails: 4, FailRate: 0.5, MinSamples: 6, TimeoutFails: 7,
		BanStatuses: []int{401, 403}, BanTTLSec: 90, BanTTLMaxSec: 900,
		MaxChannels: 50, MaxEntriesPerChan: 500,
		PickStrategy: "rr", CooldownSec: 15,
	}
	p := c.ToPolicy()
	if !p.Enabled || p.KeyMode != "host" || p.WindowSec != 240 {
		t.Errorf("basics not mapped: %+v", p)
	}
	if p.ConsecutiveFails != 4 || p.FailRate != 0.5 || p.MinSamples != 6 || p.TimeoutFails != 7 {
		t.Errorf("rules not mapped: %+v", p)
	}
	if len(p.BanStatuses) != 2 || p.BanStatuses[0] != 401 {
		t.Errorf("BanStatuses not mapped: %v", p.BanStatuses)
	}
	if p.BanTTLSec != 90 || p.BanTTLMaxSec != 900 {
		t.Errorf("TTLs not mapped: %+v", p)
	}
	if p.MaxChannels != 50 || p.MaxEntriesPerChan != 500 {
		t.Errorf("caps not mapped: %+v", p)
	}
	if p.PickStrategy != chanpolicy.StrategyRR || p.CooldownSec != 15 {
		t.Errorf("selection knobs not mapped: %+v", p)
	}
}

func TestCooldownDuration(t *testing.T) {
	if d := (ChannelPolicyConfig{CooldownSec: 0}).CooldownDuration(); d != 0 {
		t.Errorf("0 seconds = %v, want 0", d)
	}
	if d := (ChannelPolicyConfig{CooldownSec: 30}).CooldownDuration(); d.Seconds() != 30 {
		t.Errorf("30 seconds = %v", d)
	}
}

// The config has to survive a Marshal/Parse round trip, since that is how the
// panel saves it.
func TestChannelConfigRoundTrip(t *testing.T) {
	original := Default()
	original.Channels.WindowSec = 111
	original.Channels.PickStrategy = "rr"
	original.Channels.BanStatuses = []int{403, 429, 503}

	raw := original.Marshal()
	back := Parse(raw)
	if back.Channels.WindowSec != 111 {
		t.Errorf("WindowSec = %d after round trip, want 111", back.Channels.WindowSec)
	}
	if back.Channels.PickStrategy != "rr" {
		t.Errorf("PickStrategy = %q after round trip", back.Channels.PickStrategy)
	}
	if len(back.Channels.BanStatuses) != 3 {
		t.Errorf("BanStatuses = %v after round trip", back.Channels.BanStatuses)
	}
	// And the marshalled form must be valid JSON containing the block.
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		t.Fatalf("Marshal produced invalid JSON: %v", err)
	}
	if _, ok := probe["channels"]; !ok {
		t.Errorf("marshalled config has no channels block: %s", raw)
	}
}
