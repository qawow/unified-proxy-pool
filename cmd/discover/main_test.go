package main

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"unified-proxy-pool/internal/crawlers"
)

// bestParse picks the format to record in sources.go, so a wrong answer here
// mislabels a source: the panel's format column lies, and -emit-go writes the
// wrong constructor.
//
// The subtlety: dynamicCrawler.Parse falls back to ExtractIPPort for the json,
// jsonl and html_table formats when the structured parse yields nothing. So on a
// plain host:port list every format returns the same count, and the winner is
// decided entirely by tie-breaking.
func TestBestParsePrefersSimplestFormatOnTie(t *testing.T) {
	plainList := "1.2.3.4:8080\n5.6.7.8:3128\n9.10.11.12:80\n"

	format, items := bestParse([]byte(plainList), "http")
	if len(items) != 3 {
		t.Fatalf("expected 3 proxies, got %d", len(items))
	}
	if format != "plaintext" {
		t.Errorf("a plain host:port list was reported as %q; every format ties here via the "+
			"ExtractIPPort fallback, so the simplest truthful one must win", format)
	}
}

// A body only the JSON parser can read must be reported as json: the regex
// cannot match host and port that are in separate fields.
func TestBestParseDetectsRealJSON(t *testing.T) {
	body := `{"data":[{"ip":"1.2.3.4","port":"8080"},{"ip":"5.6.7.8","port":3128}]}`
	format, items := bestParse([]byte(body), "http")
	if len(items) != 2 {
		t.Fatalf("expected 2 proxies, got %d: %+v", len(items), items)
	}
	if format != "json" {
		t.Errorf("separate host/port fields must be detected as json, got %q", format)
	}
}

// An HTML table that splits host and port across cells is unreadable by the
// regex, so html_table must win on count.
func TestBestParseDetectsHTMLTable(t *testing.T) {
	body := `<table><tr><th>IP</th><th>Port</th></tr>
	<tr><td>1.2.3.4</td><td>8080</td></tr>
	<tr><td>5.6.7.8</td><td>3128</td></tr></table>`
	format, items := bestParse([]byte(body), "http")
	if len(items) != 2 {
		t.Fatalf("expected 2 proxies, got %d: %+v", len(items), items)
	}
	if format != "html_table" {
		t.Errorf("split host/port cells must be detected as html_table, got %q", format)
	}
}

// Junk must not be reported as parseable, or a dead endpoint looks like a source.
func TestBestParseRejectsJunk(t *testing.T) {
	for _, body := range []string{"", "<html><body>403 Forbidden</body></html>", "not a proxy list"} {
		_, items := bestParse([]byte(body), "http")
		if len(items) != 0 {
			t.Errorf("junk body %q yielded %+v", body, items)
		}
	}
}

// The emitted declaration has to compile and name the right constructor, since
// the whole point of -emit-go is pasting it into sources.go unedited.
func TestGhWrapRendersSourcesGoStyle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://raw.githubusercontent.com/owner/repo/main/http.txt", `gh("owner/repo/main/http.txt")`},
		// The mirror prefix must be stripped: sources.go adds it via gh().
		{mirrorPrefix + "https://raw.githubusercontent.com/owner/repo/main/http.txt", `gh("owner/repo/main/http.txt")`},
		{"https://example.com/list.txt", `"https://example.com/list.txt"`},
	}
	for _, c := range cases {
		if got := ghWrap(c.in); got != c.want {
			t.Errorf("ghWrap(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestNameFromURL(t *testing.T) {
	cases := []struct {
		url, proto, want string
	}{
		{"https://raw.githubusercontent.com/SoliSpirit/proxy-list/main/http.txt", "http", "solispirit-http"},
		{"https://raw.githubusercontent.com/MuRongPIG/Proxy-Master/main/socks5.txt", "socks5", "murongpig-socks5"},
		{"https://api.proxyscrape.com/v2/?request=displayproxies", "socks4", "api-socks4"},
		{"https://www.proxy-list.download/api/v1/get?type=http", "http", "proxy-list-http"},
	}
	for _, c := range cases {
		if got := nameFromURL(c.url, c.proto); got != c.want {
			t.Errorf("nameFromURL(%q, %q) = %q, want %q", c.url, c.proto, got, c.want)
		}
	}
}

// Candidates must be scored against each other, not only against the baseline.
//
// These lists mirror one another constantly: probing SoliSpirit/proxy-list and
// MuRongPIG/Proxy-Master showed all 101,628 of MuRongPIG's addresses already
// present in SoliSpirit's 122,777, yet both scored ~74k novel against the
// baseline and both were reported KEEP. Adding both doubles the fetch cost for
// zero extra addresses.
func TestScoreMarginalMarksSubsetRedundant(t *testing.T) {
	big := findingWith("big", []string{"1.1.1.1:80", "2.2.2.2:80", "3.3.3.3:80", "4.4.4.4:80"})
	subset := findingWith("subset", []string{"1.1.1.1:80", "2.2.2.2:80"})
	baseline := map[string]struct{}{}

	got := scoreMarginal([]finding{subset, big}, baseline)

	// Highest novelty is processed first and keeps its full count.
	if got[0].cand.nameHnt != "big" {
		t.Fatalf("expected the larger list first, got %q", got[0].cand.nameHnt)
	}
	if got[0].marginal != 4 {
		t.Errorf("the first source should claim all 4 addresses, got %d", got[0].marginal)
	}
	// The subset adds nothing once the superset is accepted.
	if got[1].marginal != 0 {
		t.Errorf("a strict subset must have 0 marginal value, got %d", got[1].marginal)
	}
	if v := got[1].verdict(true); v != "REDUNDANT" {
		t.Errorf("a strict subset must be REDUNDANT, got %s", v)
	}
}

// Partial overlap must be reported as the incremental gain, not the full count.
func TestScoreMarginalReportsIncrementalGain(t *testing.T) {
	a := findingWith("a", []string{"1.1.1.1:80", "2.2.2.2:80", "3.3.3.3:80"})
	b := findingWith("b", []string{"2.2.2.2:80", "3.3.3.3:80", "9.9.9.9:80"})

	got := scoreMarginal([]finding{a, b}, map[string]struct{}{})

	if got[0].marginal != 3 {
		t.Errorf("first source marginal = %d, want 3", got[0].marginal)
	}
	if got[1].marginal != 1 {
		t.Errorf("second source overlaps on 2 of 3, marginal = %d, want 1", got[1].marginal)
	}
}

// Addresses already in the baseline must not be credited to any candidate.
func TestScoreMarginalExcludesBaseline(t *testing.T) {
	f := findingWith("f", []string{"1.1.1.1:80", "2.2.2.2:80"})
	baseline := map[string]struct{}{"1.1.1.1:80": {}}

	got := scoreMarginal([]finding{f}, baseline)
	if got[0].marginal != 1 {
		t.Errorf("marginal = %d, want 1 (one of two already known)", got[0].marginal)
	}
}

// findingWith builds a finding holding the given "host:port" addresses.
func findingWith(name string, addrs []string) finding {
	f := finding{
		cand:     candidate{rawURL: "https://example.invalid/" + name, nameHnt: name},
		bytes:    1,
		byProto:  map[string]int{},
		byFamily: map[string]int{},
	}
	for _, a := range addrs {
		host, portText, err := net.SplitHostPort(a)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			continue
		}
		f.proxies = append(f.proxies, crawlers.Proxy{Host: host, Port: port, Protocol: "http"})
	}
	f.novel = len(f.proxies)
	return f
}

// A candidate already in sources.go is not a discovery, however it is spelled.
func TestNormalizeURLKeyIgnoresMirrorAndScheme(t *testing.T) {
	direct := "https://raw.githubusercontent.com/owner/repo/main/http.txt"
	variants := []string{
		direct,
		mirrorPrefix + direct,
		"http://raw.githubusercontent.com/owner/repo/main/http.txt",
		direct + "/",
	}
	want := normalizeURLKey(direct)
	for _, v := range variants {
		if got := normalizeURLKey(v); got != want {
			t.Errorf("normalizeURLKey(%q) = %q, want %q", v, got, want)
		}
	}
}

// protoFromURL must not let a substring win: "socks5.txt" contains "socks".
func TestProtoFromURLPrefersLongestMatch(t *testing.T) {
	cases := []struct{ url, want string }{
		{"https://x/socks5.txt", "socks5"},
		{"https://x/socks4.txt", "socks4"},
		{"https://x/http.txt", "http"},
		// The pool dials https upstreams as http proxies.
		{"https://x/https.txt", "http"},
		{"https://x/proxies.txt", ""},
	}
	for _, c := range cases {
		if got := protoFromURL(c.url); got != c.want {
			t.Errorf("protoFromURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestReadCandidatesParsesHintForms(t *testing.T) {
	in := strings.Join([]string{
		"# comment",
		"",
		"https://x/http.txt",
		"https://x/list.txt socks5",
		"myname=https://x/other.txt",
		"https://x/http.txt", // duplicate, must collapse
	}, "\n")

	got, err := readCandidatesFrom(strings.NewReader(in))
	if err != nil {
		t.Fatalf("readCandidatesFrom: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 candidates, got %d: %+v", len(got), got)
	}
	if got[1].protoHnt != "socks5" {
		t.Errorf("inline protocol hint lost: %+v", got[1])
	}
	if got[2].nameHnt != "myname" {
		t.Errorf("explicit name hint lost: %+v", got[2])
	}
}
