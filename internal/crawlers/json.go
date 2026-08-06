package crawlers

import (
	"encoding/json"
	"net"
	"strconv"
	"strings"
)

// Field names seen across the free JSON APIs this pool reads. Sources rename
// these constantly, so matching a set of aliases is more durable than writing
// one parser per site.
var (
	jsonHostKeys  = []string{"ip", "host", "hostname", "server", "address", "ip_address", "ipAddress"}
	jsonPortKeys  = []string{"port", "portNumber", "port_number"}
	jsonAddrKeys  = []string{"proxy", "addr", "address", "ip_port", "ipPort", "endpoint", "url"}
	jsonProtoKeys = []string{"protocol", "type", "scheme", "proxy_type", "proxyType"}
)

// ParseJSONProxies extracts proxies from the JSON shapes the free APIs use:
// a bare array, an object with a "data"/"proxies"/... array, or JSON Lines
// (one object per line, which is what fatezero serves). It handles both
// separate host+port fields and a pre-joined "1.2.3.4:8080" address.
//
// The regex extractor cannot cover these: it needs host and port adjacent in
// the text, and several default-enabled sources publish them as separate fields.
//
// defaultProto applies to entries that do not name their own protocol.
// Returns nil when the body is not JSON, so callers can fall back to the regex.
func ParseJSONProxies(body []byte, defaultProto string) []Proxy {
	if defaultProto == "" {
		defaultProto = "http"
	}
	out := make([]Proxy, 0, 64)
	seen := map[string]struct{}{}

	add := func(entry map[string]any) {
		p, ok := proxyFromJSONObject(entry, defaultProto)
		if !ok {
			return
		}
		key := net.JoinHostPort(p.Host, strconv.Itoa(p.Port)) + "|" + p.Protocol
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil
	}

	// Whole-document parse first: a bare array or a wrapper object.
	var doc any
	if err := json.Unmarshal([]byte(trimmed), &doc); err == nil {
		for _, entry := range collectJSONObjects(doc, 0) {
			add(entry)
		}
		return out
	}

	// JSON Lines: each line is its own document.
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		for _, entry := range collectJSONObjects(item, 0) {
			add(entry)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// collectJSONObjects walks arrays and wrapper objects to find the entry objects.
// depth is bounded so a deeply nested or hostile payload cannot blow the stack.
func collectJSONObjects(node any, depth int) []map[string]any {
	if depth > 6 {
		return nil
	}
	switch v := node.(type) {
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			out = append(out, collectJSONObjects(item, depth+1)...)
		}
		return out
	case map[string]any:
		// An object that itself looks like a proxy entry is a leaf.
		if _, ok := firstJSONString(v, jsonHostKeys); ok {
			return []map[string]any{v}
		}
		if _, ok := firstJSONString(v, jsonAddrKeys); ok {
			return []map[string]any{v}
		}
		// Otherwise it is a wrapper: descend into its array/object values.
		var out []map[string]any
		for _, val := range v {
			switch val.(type) {
			case []any, map[string]any:
				out = append(out, collectJSONObjects(val, depth+1)...)
			}
		}
		return out
	default:
		return nil
	}
}

func proxyFromJSONObject(entry map[string]any, defaultProto string) (Proxy, bool) {
	proto := defaultProto
	if raw, ok := firstJSONString(entry, jsonProtoKeys); ok {
		if norm := normalizeSourceProto(raw); norm != "" {
			proto = norm
		}
	} else if list, ok := entry["protocols"].([]any); ok && len(list) > 0 {
		if s, ok := list[0].(string); ok {
			if norm := normalizeSourceProto(s); norm != "" {
				proto = norm
			}
		}
	}

	// Separate host + port fields.
	if host, ok := firstJSONString(entry, jsonHostKeys); ok {
		if port, ok := firstJSONInt(entry, jsonPortKeys); ok {
			if h, p, ok := validHostPort(host, port); ok {
				return Proxy{Host: h, Port: p, Protocol: proto}, true
			}
		}
	}

	// Pre-joined "host:port" (or a full URL) in a single field.
	if addr, ok := firstJSONString(entry, jsonAddrKeys); ok {
		if h, p, ok := splitHostPort(stripURLScheme(addr)); ok {
			return Proxy{Host: h, Port: p, Protocol: proto}, true
		}
	}
	return Proxy{}, false
}

// validHostPort accepts only IP literals, matching splitHostPort: these sources
// publish addresses, and a hostname would need resolving before it is usable.
func validHostPort(host string, port int) (string, int, bool) {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	if host == "" || port <= 0 || port > 65535 {
		return "", 0, false
	}
	if net.ParseIP(host) == nil {
		return "", 0, false
	}
	return host, port, true
}

func stripURLScheme(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// Drop any path/query the source appended.
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

func firstJSONString(entry map[string]any, keys []string) (string, bool) {
	for _, k := range keys {
		v, ok := entry[k]
		if !ok {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s), true
		}
	}
	return "", false
}

// firstJSONInt accepts both 8080 and "8080": these APIs disagree on whether the
// port is a number or a string, sometimes within the same response.
func firstJSONInt(entry map[string]any, keys []string) (int, bool) {
	for _, k := range keys {
		v, ok := entry[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case float64:
			return int(t), true
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(t))
			if err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// normalizeSourceProto maps the many spellings sources use onto what the pool
// stores. Unknown values return "" so the source default is kept.
func normalizeSourceProto(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "http":
		return "http"
	case "https", "http/https", "https/http":
		return "https"
	case "socks5", "socks5h", "socks 5":
		return "socks5"
	case "socks4", "socks4a", "socks 4":
		return "socks4"
	case "socks":
		return "socks5"
	default:
		return ""
	}
}

type jsonCrawler struct {
	name           string
	urls           []string
	protocol       string
	fragile        bool
	defaultEnabled bool
}

// JSONSource reads proxies from a JSON or JSON Lines API, falling back to the
// regex extractor if the body turns out not to be JSON.
func JSONSource(name string, urls []string, protocol string, fragile, enabled bool) Crawler {
	if protocol == "" {
		protocol = "http"
	}
	return &jsonCrawler{name: name, urls: urls, protocol: protocol, fragile: fragile, defaultEnabled: enabled}
}

func (c *jsonCrawler) Name() string         { return c.name }
func (c *jsonCrawler) URLs() []string       { return c.urls }
func (c *jsonCrawler) Protocol() string     { return c.protocol }
func (c *jsonCrawler) Fragile() bool        { return c.fragile }
func (c *jsonCrawler) DefaultEnabled() bool { return c.defaultEnabled }
func (c *jsonCrawler) Builtin() bool        { return true }
func (c *jsonCrawler) Format() string       { return "json" }

func (c *jsonCrawler) Parse(body []byte, _ string) ([]Proxy, error) {
	if items := ParseJSONProxies(body, c.protocol); len(items) > 0 {
		return items, nil
	}
	// Not JSON (or JSON we could not read): a plain list is the common
	// alternative, so degrade instead of returning nothing.
	items := ExtractIPPort(string(body))
	for i := range items {
		items[i].Protocol = c.protocol
	}
	return items, nil
}
