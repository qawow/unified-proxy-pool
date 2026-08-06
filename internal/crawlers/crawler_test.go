package crawlers

import (
	"fmt"
	"testing"
)

// ExtractIPPort is the choke point every source funnels through, so anything it
// cannot represent is unreachable for the whole pool no matter how many sources
// are configured.

func TestExtractIPPortIPv4(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // "host:port", empty means "must extract nothing"
	}{
		{"bare line", "1.2.3.4:8080", "1.2.3.4:8080"},
		{"with junk around", "proxy => 1.2.3.4:8080 (elite)", "1.2.3.4:8080"},
		{"tab separated", "1.2.3.4\t8080", ""},
		{"octet out of range", "1.2.3.999:8080", ""},
		{"port out of range", "1.2.3.4:99999", ""},
		{"no port", "1.2.3.4", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractIPPort(tc.body)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("expected nothing, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("expected exactly 1 proxy, got %+v", got)
			}
			if addr := fmt.Sprintf("%s:%d", got[0].Host, got[0].Port); addr != tc.want {
				t.Errorf("got %s, want %s", addr, tc.want)
			}
		})
	}
}

// Leading-zero octets pass a naive 0-255 range check but net.ParseIP rejects
// them, so they reach the store with ip_family=unknown and can never be dialed:
// the dialer falls through to DNS and "103.250.166.04" does not resolve.
// Found in SoliSpirit/proxy-list, which ships one per 122k entries.
func TestExtractIPPortRejectsLeadingZeroOctets(t *testing.T) {
	for _, body := range []string{
		"103.250.166.04:6667",
		"1.2.3.04:8080",
		"01.2.3.4:8080",
		"1.02.3.4:8080",
	} {
		if got := ExtractIPPort(body); len(got) != 0 {
			t.Errorf("%q is not a dialable address and must be rejected, got %+v", body, got)
		}
	}
	// The same address without the padding is fine.
	if got := ExtractIPPort("103.250.166.4:6667"); len(got) != 1 {
		t.Errorf("expected the unpadded form to be accepted, got %+v", got)
	}
}

func TestExtractIPPortDeduplicates(t *testing.T) {
	got := ExtractIPPort("1.2.3.4:8080\n1.2.3.4:8080\n1.2.3.4:8081")
	if len(got) != 2 {
		t.Errorf("expected 2 unique proxies, got %+v", got)
	}
}

// The store, API and UI all distinguish IPv4 from IPv6, but none of that can
// ever see an IPv6 proxy if the extractor drops it. Bracketed host:port is the
// form every published list uses, since a bare IPv6 literal is ambiguous with
// its own colons.
func TestExtractIPPortIPv6(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"bracketed", "[2001:db8::1]:1080", "2001:db8::1:1080"},
		{"bracketed full", "[2001:0db8:0000:0000:0000:0000:0000:0001]:8080", "2001:0db8:0000:0000:0000:0000:0000:0001:8080"},
		{"bracketed loopback", "[::1]:3128", "::1:3128"},
		{"bracketed in a table row", "| [2606:4700::1]:8443 | elite |", "2606:4700::1:8443"},
		{"ipv4-mapped", "[::ffff:1.2.3.4]:8080", "::ffff:1.2.3.4:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractIPPort(tc.body)
			if len(got) != 1 {
				t.Fatalf("expected exactly 1 proxy from %q, got %+v", tc.body, got)
			}
			if addr := fmt.Sprintf("%s:%d", got[0].Host, got[0].Port); addr != tc.want {
				t.Errorf("got %s, want %s", addr, tc.want)
			}
		})
	}
}

// A bare IPv6 literal without brackets has no unambiguous port boundary, so it
// must be rejected rather than guessed at.
func TestExtractIPPortRejectsUnbracketedIPv6(t *testing.T) {
	for _, body := range []string{"2001:db8::1:1080", "::1:8080"} {
		if got := ExtractIPPort(body); len(got) != 0 {
			t.Errorf("%q is ambiguous and must be rejected, got %+v", body, got)
		}
	}
}

// Mixed lists are the common case once IPv6 sources are enabled.
func TestExtractIPPortMixedFamilies(t *testing.T) {
	body := "1.2.3.4:8080\n[2001:db8::1]:1080\n5.6.7.8:3128\n"
	got := ExtractIPPort(body)
	if len(got) != 3 {
		t.Fatalf("expected 3 proxies, got %+v", got)
	}
	var v6 int
	for _, p := range got {
		if p.Host == "2001:db8::1" {
			v6++
		}
	}
	if v6 != 1 {
		t.Errorf("the IPv6 entry was dropped from a mixed list: %+v", got)
	}
}
