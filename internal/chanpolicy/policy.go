package chanpolicy

import "time"

// Policy holds the tunables. It mirrors features.ChannelPolicy so the panel can
// hot-apply changes; Normalize() fills in anything the user left at zero.
type Policy struct {
	Enabled bool
	KeyMode string

	// WindowSec is the sliding window every counter is measured over.
	WindowSec int

	// A proxy is banned for a channel when ANY of these trips. Set a field to 0
	// to disable that rule alone.
	ConsecutiveFails int     // consecutive failures inside the window
	FailRate         float64 // 0-1, needs MinSamples before it can fire
	MinSamples       int
	TimeoutFails     int   // timeouts alone, counted separately from refusals
	BanStatuses      []int // HTTP statuses that ban on a single sighting

	// Ban duration doubles per repeat offence, capped at BanTTLMaxSec.
	BanTTLSec    int
	BanTTLMaxSec int

	// Memory ceilings. Channels are keyed by destination, so an unbounded map is
	// a slow leak on any pool serving varied traffic.
	MaxChannels       int
	MaxEntriesPerChan int

	// Selection knobs, read by internal/freproxies.
	PickStrategy string
	CooldownSec  int

	// LogRetainHours is how long persisted request logs are kept. 0 = 48h.
	LogRetainHours int
	// ReprobeOnExpiry keeps the ban in force after TTL until a later success
	// is recorded, so a just-403'd IP is not handed straight back.
	ReprobeOnExpiry bool
}

// Selection strategies.
const (
	StrategyWeighted = "weighted"
	StrategyRandom   = "random"
	StrategyRR       = "rr"
)

// Defaults returns the shipped policy.
func Defaults() Policy {
	return Policy{
		Enabled:           true,
		KeyMode:           KeyETLD1,
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
		PickStrategy:      StrategyWeighted,
		CooldownSec:       30,
	}
}

// Normalize repairs out-of-range values instead of rejecting them: this config
// arrives from a hand-edited JSON blob, and a typo should degrade to the default
// rather than disable banning.
func (p Policy) Normalize() Policy {
	d := Defaults()
	if p.KeyMode != KeyETLD1 && p.KeyMode != KeyHost && p.KeyMode != KeyOff {
		p.KeyMode = d.KeyMode
	}
	if p.WindowSec <= 0 {
		p.WindowSec = d.WindowSec
	}
	if p.WindowSec > 3600 {
		p.WindowSec = 3600
	}
	// Negative values mean "off" for rules, so only repair those.
	if p.ConsecutiveFails < 0 {
		p.ConsecutiveFails = 0
	}
	if p.FailRate < 0 || p.FailRate > 1 {
		p.FailRate = d.FailRate
	}
	if p.MinSamples <= 0 {
		p.MinSamples = d.MinSamples
	}
	if p.TimeoutFails < 0 {
		p.TimeoutFails = 0
	}
	if p.BanTTLSec <= 0 {
		p.BanTTLSec = d.BanTTLSec
	}
	if p.BanTTLMaxSec < p.BanTTLSec {
		p.BanTTLMaxSec = p.BanTTLSec
	}
	if p.MaxChannels <= 0 {
		p.MaxChannels = d.MaxChannels
	}
	if p.MaxEntriesPerChan <= 0 {
		p.MaxEntriesPerChan = d.MaxEntriesPerChan
	}
	if p.PickStrategy != StrategyWeighted && p.PickStrategy != StrategyRandom && p.PickStrategy != StrategyRR {
		p.PickStrategy = d.PickStrategy
	}
	if p.CooldownSec < 0 {
		p.CooldownSec = 0
	}
	if p.LogRetainHours <= 0 {
		p.LogRetainHours = d.LogRetainHours
	}
	return p
}

func (p Policy) window() time.Duration { return time.Duration(p.WindowSec) * time.Second }

func (p Policy) bansStatus(status int) bool {
	if status <= 0 {
		return false
	}
	for _, s := range p.BanStatuses {
		if s == status {
			return true
		}
	}
	return false
}

// idleResetAfter is how long a pair must go without a new failure before its
// escalation ladder returns to the base TTL. Without this a proxy that misbehaves
// once an hour would creep up to the maximum ban and stay there.
func (p Policy) idleResetAfter() time.Duration {
	d := 10 * time.Minute
	if w := 2 * p.window(); w > d {
		d = w
	}
	return d
}
