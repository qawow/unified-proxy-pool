package chanpolicy

import "testing"

func TestNormalizeETLD1FoldsSubdomains(t *testing.T) {
	// The whole point of the default mode: a site rate-limits per site, so every
	// host under one registrable domain has to share a ban.
	cases := []struct{ in, want string }{
		{"item.taobao.com", "taobao.com"},
		{"www.taobao.com", "taobao.com"},
		{"taobao.com", "taobao.com"},
		{"a.b.c.taobao.com:443", "taobao.com"},
		{"https://item.taobao.com/x?y=1", "taobao.com"},
		// Multi-label public suffixes must not be over-folded to "co.uk".
		{"shop.a.b.co.uk", "b.co.uk"},
	}
	for _, c := range cases {
		if got := Normalize(c.in, KeyETLD1); got != c.want {
			t.Errorf("Normalize(%q, etld1) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeHostModeKeepsSubdomains(t *testing.T) {
	if got := Normalize("item.taobao.com:443", KeyHost); got != "item.taobao.com" {
		t.Errorf("host mode = %q, want item.taobao.com", got)
	}
	if got := Normalize("www.taobao.com", KeyHost); got != "www.taobao.com" {
		t.Errorf("host mode = %q, want www.taobao.com", got)
	}
}

func TestNormalizeOffModeCollapsesEverything(t *testing.T) {
	for _, in := range []string{"item.taobao.com", "amazon.com", "1.2.3.4:80"} {
		if got := Normalize(in, KeyOff); got != DefaultChannel {
			t.Errorf("Normalize(%q, off) = %q, want %q", in, got, DefaultChannel)
		}
	}
}

// Hosts with no registrable domain must fall back to the raw host rather than
// collapsing into one bucket — otherwise every IP-addressed target would share
// a single channel and ban each other's proxies.
func TestNormalizeNoRegistrableDomainFallsBackToHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.2.3.4", "1.2.3.4"},
		{"1.2.3.4:8080", "1.2.3.4"},
		{"localhost", "localhost"},
		{"localhost:3000", "localhost"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"2001:db8::1", "2001:db8::1"},
	}
	for _, c := range cases {
		if got := Normalize(c.in, KeyETLD1); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeStripsSchemeUserinfoPathAndCase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"HTTPS://User:Pa@ss@Item.Taobao.COM:443/path#f", "taobao.com"},
		{"taobao.com.", "taobao.com"},
		{"  taobao.com  ", "taobao.com"},
		{"http://taobao.com", "taobao.com"},
	}
	for _, c := range cases {
		if got := Normalize(c.in, KeyETLD1); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeEmptyYieldsDefaultChannel(t *testing.T) {
	for _, in := range []string{"", "   ", "://", "http://"} {
		if got := Normalize(in, KeyETLD1); got != DefaultChannel {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, DefaultChannel)
		}
	}
}

// A caller passing ?channel=https://item.taobao.com/x must land on the same key
// as one passing ?target= for the same place, or reports would file against a
// channel that selection never consults.
func TestNormalizeChannelNameMatchesDerivedChannel(t *testing.T) {
	derived := Normalize("item.taobao.com:443", KeyETLD1)
	for _, in := range []string{"taobao.com", "TAOBAO.COM", "taobao.com."} {
		if got := NormalizeChannelName(in); got != derived {
			t.Errorf("NormalizeChannelName(%q) = %q, want %q", in, got, derived)
		}
	}
	// URL-ish channel names get reduced to a host, not stored verbatim.
	if got := NormalizeChannelName("https://item.taobao.com/x"); got != "item.taobao.com" {
		t.Errorf("NormalizeChannelName(url) = %q, want item.taobao.com", got)
	}
	if got := NormalizeChannelName("1.2.3.4:8080"); got != "1.2.3.4" {
		t.Errorf("NormalizeChannelName(host:port) = %q, want 1.2.3.4", got)
	}
}

func TestNormalizeUnknownKeyModeFallsBackToETLD1(t *testing.T) {
	if got := Normalize("item.taobao.com", "bogus"); got != "taobao.com" {
		t.Errorf("unknown key mode = %q, want taobao.com (etld1 behaviour)", got)
	}
}
