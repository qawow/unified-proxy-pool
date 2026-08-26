// Package chanpolicy tracks per-channel proxy outcomes and temporarily bans a
// proxy for one channel without touching the others.
//
// A "channel" is the destination a request is headed for. Banning is scoped to
// it because a proxy that a site has rate-limited is still perfectly usable
// everywhere else — the global blacklist (internal/blacklist) is the hard,
// admin-owned ban; this is the soft, automatic, expiring one.
package chanpolicy

import (
	"net"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Channel key derivation modes.
const (
	// KeyETLD1 folds every host under one registrable domain into a single
	// channel: item.taobao.com and www.taobao.com share a ban. This matches how
	// sites actually rate-limit (per-site, not per-subdomain).
	KeyETLD1 = "etld1"
	// KeyHost keeps each host separate.
	KeyHost = "host"
	// KeyOff disables channel scoping — everything lands in DefaultChannel,
	// which makes bans behave globally.
	KeyOff = "off"
)

// DefaultChannel receives traffic whose channel cannot be determined, and all
// traffic when KeyMode is KeyOff.
const DefaultChannel = "default"

// Normalize derives a channel name from a request target.
//
// The target may be a bare host, host:port, or a full URL. Ports, schemes,
// paths, userinfo, trailing dots and case are all stripped. IP literals and
// single-label names (localhost) have no registrable domain, so they are used
// as-is rather than discarded.
func Normalize(target, mode string) string {
	host := hostOf(target)
	if host == "" {
		return DefaultChannel
	}
	switch mode {
	case KeyOff:
		return DefaultChannel
	case KeyHost:
		return host
	default: // KeyETLD1 and anything unrecognized
		if domain, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil && domain != "" {
			return domain
		}
		// IP literals, localhost, .onion, and hosts whose suffix isn't in the
		// public list land here. The raw host is still a stable key.
		return host
	}
}

// hostOf extracts a bare lowercase hostname from a bare host, host:port, or URL.
func hostOf(target string) string {
	s := strings.TrimSpace(target)
	if s == "" {
		return ""
	}
	// Strip scheme without url.Parse: callers pass CONNECT targets ("host:443")
	// far more often than URLs, and url.Parse reads a bare "host:443" as
	// scheme="host" opaque="443".
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// Drop path/query/fragment.
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	// Drop userinfo. LastIndex, because a password may contain '@'.
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Bracketed IPv6, with or without a port.
	if strings.HasPrefix(s, "[") {
		if i := strings.Index(s, "]"); i > 0 {
			return canonicalHost(s[1:i])
		}
		return canonicalHost(strings.TrimPrefix(s, "["))
	}
	// Bare IPv6 has multiple colons and no port — SplitHostPort would reject it,
	// and naive colon-splitting would truncate it.
	if strings.Count(s, ":") > 1 {
		return canonicalHost(s)
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		return canonicalHost(host)
	}
	return canonicalHost(s)
}

// canonicalHost lowercases and strips the trailing root dot so "Taobao.com."
// and "taobao.com" are one channel.
func canonicalHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if len(h) > 1 {
		h = strings.TrimSuffix(h, ".")
	}
	return h
}

// NormalizeChannelName cleans a channel name supplied directly by a caller (via
// ?channel= or a report body) so it cannot diverge from a derived one.
func NormalizeChannelName(name string) string {
	n := canonicalHost(name)
	if n == "" {
		return ""
	}
	// A caller passing a URL or host:port as ?channel= means the same thing as
	// passing it as ?target=; treat it that way instead of storing a key that
	// could never match a derived channel.
	if strings.ContainsAny(n, "/:@") {
		return hostOf(n)
	}
	return n
}
