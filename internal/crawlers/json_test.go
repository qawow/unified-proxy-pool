package crawlers

import (
	"fmt"
	"sort"
	"testing"
)

// renderAll gives a stable, readable form for comparing extraction results.
func renderAll(items []Proxy) []string {
	out := make([]string, 0, len(items))
	for _, p := range items {
		out = append(out, fmt.Sprintf("%s|%s|%d", p.Protocol, p.Host, p.Port))
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Sources that publish host and port as separate JSON fields cannot be read by
// the IP:port regex at all — it needs the two joined by a colon in the text.
// geonode and fatezero are both default-enabled, so this is not hypothetical.
func TestParseJSONProxiesSeparateHostPortFields(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			// geonode: {"data":[{"ip":"1.2.3.4","port":"8080"}]} — port is a string.
			name: "geonode nested data array",
			body: `{"data":[{"ip":"1.2.3.4","port":"8080","protocols":["http"]},
			         {"ip":"5.6.7.8","port":"3128","protocols":["socks5"]}]}`,
			want: []string{"http|1.2.3.4|8080", "socks5|5.6.7.8|3128"},
		},
		{
			// fatezero: one JSON object per line, port is a number.
			name: "fatezero jsonl",
			body: "{\"host\":\"1.2.3.4\",\"port\":8080,\"type\":\"http\"}\n" +
				"{\"host\":\"5.6.7.8\",\"port\":1080,\"type\":\"socks5\"}\n",
			want: []string{"http|1.2.3.4|8080", "socks5|5.6.7.8|1080"},
		},
		{
			name: "bare array of objects",
			body: `[{"ip":"1.2.3.4","port":8080},{"ip":"5.6.7.8","port":3128}]`,
			want: []string{"http|1.2.3.4|8080", "http|5.6.7.8|3128"},
		},
		{
			// proxifly-style: the address is already joined, under varying keys.
			name: "joined address field",
			body: `[{"proxy":"1.2.3.4:8080","protocol":"http"},{"addr":"5.6.7.8:1080","protocol":"socks5"}]`,
			want: []string{"http|1.2.3.4|8080", "socks5|5.6.7.8|1080"},
		},
		{
			name: "ipv6 host field",
			body: `[{"ip":"2001:db8::1","port":1080,"protocol":"socks5"}]`,
			want: []string{"socks5|2001:db8::1|1080"},
		},
		{
			name: "ipv6 joined and bracketed",
			body: `[{"proxy":"[2001:db8::1]:1080"}]`,
			want: []string{"http|2001:db8::1|1080"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderAll(ParseJSONProxies([]byte(tc.body), "http"))
			if !equalStrings(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Payloads captured from the live endpoints on 2026-08-06. These two sources are
// default-enabled and were returning 46KB and 194KB of proxies that the regex
// extractor matched zero of, because neither joins host and port in the text.
func TestParseJSONProxiesRealPayloads(t *testing.T) {
	t.Run("geonode", func(t *testing.T) {
		body := `{"data":[{"_id":"6a6f","ip":"192.145.228.212","anonymityLevel":"transparent",` +
			`"asn":"AS140031","city":"Bandar Lampung","country":"ID","latency":172.688,` +
			`"port":"8082","protocols":["http"],"speed":5001,"upTime":100},` +
			`{"_id":"6a4e","ip":"58.210.191.173","country":"CN","port":"8816",` +
			`"protocols":["https"],"speed":282}]}`
		want := []string{"http|192.145.228.212|8082", "https|58.210.191.173|8816"}
		sort.Strings(want)
		if got := renderAll(ParseJSONProxies([]byte(body), "http")); !equalStrings(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("sunny9577", func(t *testing.T) {
		// "type":"HTTP/HTTPS" is this source's spelling; it must not be dropped
		// on the floor as an unknown protocol.
		body := `[{"ip":"103.231.239.137","port":"58080","country":"Bangladesh",
		           "anonymity":"Transparent","type":"HTTP/HTTPS"},
		          {"ip":"43.231.78.205","port":"8080","country":"Bangladesh",
		           "anonymity":"Transparent","type":"HTTP/HTTPS"}]`
		want := []string{"https|103.231.239.137|58080", "https|43.231.78.205|8080"}
		sort.Strings(want)
		if got := renderAll(ParseJSONProxies([]byte(body), "http")); !equalStrings(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

// Malformed or unexpected payloads must yield nothing rather than garbage: a
// free source can start returning an error page at any time.
func TestParseJSONProxiesRejectsJunk(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"html error page", "<html><body>403 Forbidden</body></html>"},
		{"truncated json", `{"data":[{"ip":"1.2.3.4",`},
		{"port out of range", `[{"ip":"1.2.3.4","port":99999}]`},
		{"port zero", `[{"ip":"1.2.3.4","port":0}]`},
		{"missing port", `[{"ip":"1.2.3.4"}]`},
		{"missing host", `[{"port":8080}]`},
		{"hostname not ip", `[{"ip":"proxy.example.com","port":8080}]`},
		{"empty array", `[]`},
		{"null data", `{"data":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseJSONProxies([]byte(tc.body), "http"); len(got) != 0 {
				t.Errorf("expected nothing from %q, got %+v", tc.body, got)
			}
		})
	}
}

// The per-entry protocol wins over the source default, so a mixed-protocol API
// is not flattened into one protocol.
func TestParseJSONProxiesProtocolPrecedence(t *testing.T) {
	body := `[{"ip":"1.2.3.4","port":8080},
	          {"ip":"5.6.7.8","port":1080,"protocol":"socks5"},
	          {"ip":"9.10.11.12","port":1081,"protocols":["socks4"]},
	          {"ip":"13.14.15.16","port":8081,"type":"https"}]`
	want := []string{
		"socks4|9.10.11.12|1081",
		"socks5|5.6.7.8|1080",
		"http|1.2.3.4|8080",
		"https|13.14.15.16|8081",
	}
	sort.Strings(want)
	if got := renderAll(ParseJSONProxies([]byte(body), "http")); !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Duplicates across pages/fields must collapse.
func TestParseJSONProxiesDeduplicates(t *testing.T) {
	body := `[{"ip":"1.2.3.4","port":8080},{"ip":"1.2.3.4","port":8080},{"ip":"1.2.3.4","port":8081}]`
	if got := ParseJSONProxies([]byte(body), "http"); len(got) != 2 {
		t.Errorf("expected 2 unique proxies, got %+v", got)
	}
}

// JSONSource must fall back to the regex extractor when a source that used to
// return JSON starts returning a plain list, so a format change degrades instead
// of going silent.
func TestJSONSourceFallsBackToRegex(t *testing.T) {
	c := JSONSource("t", []string{"http://example.invalid"}, "socks5", true, false)
	got, err := c.Parse([]byte("1.2.3.4:8080\n[2001:db8::1]:1080\n"), "http://example.invalid")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"socks5|1.2.3.4|8080", "socks5|2001:db8::1|1080"}
	sort.Strings(want)
	if rendered := renderAll(got); !equalStrings(rendered, want) {
		t.Errorf("got %v, want %v", rendered, want)
	}
}

// The dynamic (user-defined) json/jsonl formats must use the same parser,
// otherwise a source added through the web UI silently yields nothing.
func TestDynamicJSONFormatParsesFields(t *testing.T) {
	for _, format := range []string{"json", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			c, err := NewDynamic(DynamicSpec{
				Name: "custom", URLs: []string{"http://example.invalid"},
				Format: format, Protocol: "http",
			})
			if err != nil {
				t.Fatalf("NewDynamic: %v", err)
			}
			got, err := c.Parse([]byte(`{"data":[{"ip":"1.2.3.4","port":"8080"}]}`), "")
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if want := []string{"http|1.2.3.4|8080"}; !equalStrings(renderAll(got), want) {
				t.Errorf("got %v, want %v", renderAll(got), want)
			}
		})
	}
}

func TestParseJSONProxiesKeepsSourceCountry(t *testing.T) {
	body := `{"data":[
		{"ip":"1.2.3.4","port":"8080","country":"CN","protocols":["http"]},
		{"ip":"5.6.7.8","port":"3128","countryCode":"US","protocols":["http"]}
	]}`
	got := ParseJSONProxies([]byte(body), "http")
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2", len(got))
	}
	byHost := map[string]string{}
	for _, p := range got {
		byHost[p.Host] = p.Region
	}
	if byHost["1.2.3.4"] != "CN" {
		t.Errorf("CN source country discarded: %q", byHost["1.2.3.4"])
	}
	if byHost["5.6.7.8"] != "US" {
		t.Errorf("US source country discarded: %q", byHost["5.6.7.8"])
	}
}
