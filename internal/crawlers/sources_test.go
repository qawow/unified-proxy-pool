package crawlers

import (
	"net/url"
	"strings"
	"testing"
)

// The source list is hand-maintained, so these checks guard the mistakes that
// are easy to make in a 150-entry literal and invisible at runtime.

// NewRegistry keeps the first entry for a duplicated name and drops the rest, so
// a copy-paste name collision silently disables a source.
func TestDefaultSourcesHaveUniqueNames(t *testing.T) {
	seen := map[string]int{}
	for _, c := range DefaultSources() {
		seen[c.Name()]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("source name %q declared %d times; the later ones are dropped by NewRegistry", name, n)
		}
	}
	if got := len(NewRegistry(DefaultSources()).Names()); got != len(seen) {
		t.Errorf("registry holds %d sources but %d unique names were declared", got, len(seen))
	}
}

// A duplicated URL means two sources fetch the same bytes every round.
func TestDefaultSourcesHaveNoDuplicateURLs(t *testing.T) {
	owner := map[string]string{}
	for _, c := range DefaultSources() {
		for _, u := range c.URLs() {
			if prev, ok := owner[u]; ok {
				t.Errorf("%s and %s both fetch %s", prev, c.Name(), u)
				continue
			}
			owner[u] = c.Name()
		}
	}
}

func TestDefaultSourcesAreWellFormed(t *testing.T) {
	// What the validator and the chain dialer actually understand. A protocol
	// outside this set is dialed as plain HTTP, so a typo turns into silent
	// misrouting rather than an error.
	knownProto := map[string]bool{"http": true, "https": true, "socks4": true, "socks5": true}

	for _, c := range DefaultSources() {
		t.Run(c.Name(), func(t *testing.T) {
			if strings.TrimSpace(c.Name()) == "" {
				t.Fatal("empty source name")
			}
			if !knownProto[c.Protocol()] {
				t.Errorf("protocol %q is not one of http/https/socks4/socks5", c.Protocol())
			}
			if len(c.URLs()) == 0 {
				t.Fatal("source has no URLs")
			}
			for _, raw := range c.URLs() {
				u, err := url.Parse(raw)
				if err != nil {
					t.Errorf("unparseable URL %q: %v", raw, err)
					continue
				}
				if u.Scheme != "http" && u.Scheme != "https" {
					t.Errorf("URL %q has scheme %q, want http or https", raw, u.Scheme)
				}
				if u.Host == "" {
					t.Errorf("URL %q has no host", raw)
				}
			}
		})
	}
}

// Every source must survive a junk body: free endpoints return error pages,
// rate-limit notices and HTML redirects routinely, and a panic or error there
// would abort the scrape round for the sources that follow.
func TestDefaultSourcesToleratePlausibleJunk(t *testing.T) {
	bodies := map[string]string{
		"empty":       "",
		"html error":  "<html><head><title>403</title></head><body>Forbidden</body></html>",
		"rate limit":  `{"error":"rate limit exceeded","retry_after":60}`,
		"broken json": `{"data":[{"ip":"1.2.3.4",`,
		"binary":      "\x00\x01\x02\xff\xfe",
	}
	for _, c := range DefaultSources() {
		for label, body := range bodies {
			items, err := c.Parse([]byte(body), c.URLs()[0])
			if err != nil {
				// An error is acceptable, but it must not come with results.
				if len(items) > 0 {
					t.Errorf("%s on %s body: returned %d items alongside error %v", c.Name(), label, len(items), err)
				}
				continue
			}
			for _, p := range items {
				if p.Host == "" || p.Port <= 0 || p.Port > 65535 {
					t.Errorf("%s on %s body: produced invalid proxy %+v", c.Name(), label, p)
				}
			}
		}
	}
}

// Sources named for a protocol must actually be configured for it: the name is
// what operators filter and reason about in the UI.
func TestDefaultSourceNamesMatchProtocol(t *testing.T) {
	for _, c := range DefaultSources() {
		name := c.Name()
		switch {
		case strings.HasSuffix(name, "-socks5"):
			if c.Protocol() != "socks5" {
				t.Errorf("%s is configured as %s", name, c.Protocol())
			}
		case strings.HasSuffix(name, "-socks4"):
			if c.Protocol() != "socks4" {
				t.Errorf("%s is configured as %s", name, c.Protocol())
			}
		case strings.HasSuffix(name, "-http"):
			if c.Protocol() != "http" {
				t.Errorf("%s is configured as %s", name, c.Protocol())
			}
		}
	}
}

// A source whose URL names a protocol but whose config says another is almost
// always a copy-paste slip in the list.
func TestDefaultSourceURLsMatchProtocol(t *testing.T) {
	for _, c := range DefaultSources() {
		// Only the unambiguous cases: a URL path segment or filename that is
		// exactly a protocol keyword.
		for _, raw := range c.URLs() {
			lower := strings.ToLower(raw)
			for _, marker := range []string{"socks5", "socks4"} {
				if !strings.Contains(lower, marker) {
					continue
				}
				// "all/data.txt" style aggregate lists legitimately mix protocols.
				if strings.Contains(lower, "/all/") {
					continue
				}
				if c.Protocol() != marker {
					t.Errorf("%s fetches %s but is configured as %s", c.Name(), raw, c.Protocol())
				}
			}
		}
	}
}

func TestGithubRawMirrorsOrder(t *testing.T) {
	got := githubRawMirrors("TheSpeedX/SOCKS-List/master/http.txt")
	if len(got) != 2 {
		t.Fatalf("want ghproxy + github raw, got %v", got)
	}
	if !strings.Contains(got[0], "ghproxy.net/") {
		t.Fatalf("first mirror should be ghproxy, got %s", got[0])
	}
	if !strings.Contains(got[1], "raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/http.txt") {
		t.Fatalf("raw GitHub missing: %v", got)
	}
	for _, u := range got {
		if strings.Contains(u, "jsdelivr.net") {
			t.Fatalf("jsdelivr must not be a default mirror (Cloudflare blackhole in CN): %s", u)
		}
	}
}

func TestDefaultSourcesAvoidJsdelivr(t *testing.T) {
	for _, c := range DefaultSources() {
		for _, u := range c.URLs() {
			if strings.Contains(strings.ToLower(u), "jsdelivr.net") {
				t.Errorf("%s still fetches jsdelivr: %s", c.Name(), u)
			}
		}
	}
}
