package chanpolicy

import (
	"fmt"
	"strings"
	"time"
)

// RuleKind is one condition a custom rule can test.
const (
	RuleStatus      = "status"       // HTTP status is in Statuses
	RuleConsecutive = "consecutive"  // consecutive failures >= Threshold
	RuleFailRate    = "fail_rate"    // window fail rate >= Rate (needs MinSamples)
	RuleTimeouts    = "timeouts"     // window timeouts >= Threshold
	RuleError       = "error"        // Err tag contains Match
)

// Rule is one extra ban condition. Channel empty = applies everywhere.
// Custom rules run *in addition to* the global policy, they do not replace it.
type Rule struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Channel    string    `json:"channel,omitempty"`
	Kind       string    `json:"kind"`
	Statuses   []int     `json:"statuses,omitempty"`
	Threshold  int       `json:"threshold,omitempty"`
	Rate       float64   `json:"rate,omitempty"`
	MinSamples int       `json:"min_samples,omitempty"`
	Match      string    `json:"match,omitempty"`
	TTLSec     int       `json:"ttl_sec,omitempty"` // 0 = use global BanTTLSec
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
}

func (r Rule) appliesTo(channel string) bool {
	if !r.Enabled {
		return false
	}
	if r.Channel == "" {
		return true
	}
	return strings.EqualFold(r.Channel, channel)
}

func (r Rule) kind() string {
	switch strings.ToLower(strings.TrimSpace(r.Kind)) {
	case RuleStatus, "status_code", "http":
		return RuleStatus
	case RuleConsecutive, "consec", "streak":
		return RuleConsecutive
	case RuleFailRate, "rate":
		return RuleFailRate
	case RuleTimeouts, "timeout":
		return RuleTimeouts
	case RuleError, "err", "tag":
		return RuleError
	default:
		return ""
	}
}

func (r Rule) matches(o Outcome, e *entry, now time.Time, window time.Duration, bucketSec int64) (reason string, ok bool) {
	switch r.kind() {
	case RuleStatus:
		if o.Status <= 0 {
			return "", false
		}
		for _, s := range r.Statuses {
			if s == o.Status {
				return "rule_status_" + itoa(o.Status), true
			}
		}
	case RuleConsecutive:
		n := r.Threshold
		if n <= 0 {
			n = 3
		}
		if e.consecutive(now, window) >= n {
			return "rule_consecutive", true
		}
	case RuleFailRate:
		okN, fail, _ := e.sum(now, bucketSec)
		min := r.MinSamples
		if min <= 0 {
			min = 5
		}
		rate := r.Rate
		if rate <= 0 || rate > 1 {
			rate = 0.6
		}
		if okN+fail >= min && float64(fail)/float64(okN+fail) >= rate {
			return "rule_fail_rate", true
		}
	case RuleTimeouts:
		n := r.Threshold
		if n <= 0 {
			n = 5
		}
		_, _, timeout := e.sum(now, bucketSec)
		if timeout >= n {
			return "rule_timeouts", true
		}
	case RuleError:
		needle := strings.ToLower(strings.TrimSpace(r.Match))
		if needle == "" || o.OK {
			return "", false
		}
		if strings.Contains(strings.ToLower(o.Err), needle) {
			return "rule_error", true
		}
	}
	return "", false
}

func (r Rule) ttl(fallback Policy) time.Duration {
	if r.TTLSec > 0 {
		return time.Duration(r.TTLSec) * time.Second
	}
	return time.Duration(fallback.BanTTLSec) * time.Second
}

func normalizeRule(in Rule) (Rule, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Channel = NormalizeChannelName(in.Channel)
	in.Kind = in.kind()
	in.Match = strings.TrimSpace(in.Match)
	if in.Kind == "" {
		return Rule{}, fmt.Errorf("unknown rule kind")
	}
	if in.Kind == RuleStatus && len(in.Statuses) == 0 {
		return Rule{}, fmt.Errorf("status rule needs at least one status code")
	}
	if in.Kind == RuleError && in.Match == "" {
		return Rule{}, fmt.Errorf("error rule needs a match string")
	}
	if in.Name == "" {
		in.Name = in.Kind
	}
	if in.Rate < 0 || in.Rate > 1 {
		in.Rate = 0
	}
	if in.TTLSec < 0 {
		in.TTLSec = 0
	}
	return in, nil
}
