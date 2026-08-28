package crawlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"

	"unified-proxy-pool/internal/netutil"
)

type Proxy struct {
	Host     string
	Port     int
	Protocol string
	// Region is a source-declared country (geonode "CN", sunny9577 "Bangladesh").
	// Empty when the source did not publish one.
	Region string
}

type Crawler interface {
	Name() string
	URLs() []string
	Protocol() string
	Fragile() bool
	DefaultEnabled() bool
	Parse(body []byte, rawURL string) ([]Proxy, error)
}

type Registry struct {
	items map[string]Crawler
	order []string
}

func NewRegistry(list []Crawler) *Registry {
	r := &Registry{items: map[string]Crawler{}}
	for _, c := range list {
		if c == nil {
			continue
		}
		name := c.Name()
		if _, ok := r.items[name]; ok {
			continue
		}
		r.items[name] = c
		r.order = append(r.order, name)
	}
	return r
}

func (r *Registry) Get(name string) (Crawler, bool) {
	c, ok := r.items[name]
	return c, ok
}

func (r *Registry) All() []Crawler {
	out := make([]Crawler, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.items[name])
	}
	return out
}

func (r *Registry) Names() []string {
	return append([]string(nil), r.order...)
}

var (
	ipPortRe = regexp.MustCompile(`(?i)\b(?:\d{1,3}\.){3}\d{1,3}:\d{2,5}\b`)
	// Bracketed IPv6 is the only unambiguous inline form, and the one every
	// published list uses: a bare literal's own colons cannot be told apart from
	// the port separator. Without this pattern IPv6 proxies never reach the
	// store, which makes the ip_family field and family filters dead weight.
	ipv6PortRe = regexp.MustCompile(`\[[0-9A-Fa-f:.]{2,45}\]:\d{2,5}`)
	hostRe     = regexp.MustCompile(`^(?:\d{1,3}\.){3}\d{1,3}$`)
)

func ExtractIPPort(text string) []Proxy {
	matches := ipPortRe.FindAllString(text, -1)
	matches = append(matches, ipv6PortRe.FindAllString(text, -1)...)
	seen := map[string]struct{}{}
	out := make([]Proxy, 0, len(matches))
	for _, m := range matches {
		host, port, ok := splitHostPort(m)
		if !ok {
			continue
		}
		key := net.JoinHostPort(host, strconv.Itoa(port))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Proxy{Host: host, Port: port, Protocol: "http"})
	}
	return out
}

// splitHostPort accepts "1.2.3.4:8080" and "[2001:db8::1]:1080", returning the
// host without brackets. Only IP literals are accepted; hostnames are rejected
// because the sources this parses publish addresses, not names.
func splitHostPort(s string) (string, int, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		host, portText, err := net.SplitHostPort(s)
		if err != nil {
			return "", 0, false
		}
		if net.ParseIP(host) == nil {
			return "", 0, false
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port <= 0 || port > 65535 {
			return "", 0, false
		}
		return host, port, true
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return "", 0, false
	}
	host := parts[0]
	if !hostRe.MatchString(host) {
		return "", 0, false
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, false
	}
	// net.ParseIP is the authority on what counts as an address, and it is what
	// freproxies.DetectFamily uses — so validating with it here makes the two
	// agree by construction.
	//
	// The hand-rolled octet loop this replaces compared strconv.Atoi output
	// against 0-255, which reads "04" as 4 and let leading-zero octets through.
	// net.ParseIP rejects those, so such a host was stored with
	// ip_family=unknown and could never be dialed: the dialer falls through to
	// DNS and "103.250.166.04" does not resolve.
	if net.ParseIP(host) == nil {
		return "", 0, false
	}
	return host, port, true
}

type HTTPClient struct {
	client *http.Client
}

func NewHTTPClient(timeout time.Duration) *HTTPClient {
	return NewHTTPClientWithProxy(timeout, "")
}

func (h *HTTPClient) Get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	netutil.ApplyDefaultHeaders(req.Header)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return body, nil
}

// jsdelivrBlocked is set the first time a jsDelivr/Cloudflare fetch times out
// in this process. Later sources skip those URLs instead of burning 8s each.
var jsdelivrBlocked atomic.Bool

func isJsdelivrURL(raw string) bool {
	return strings.Contains(strings.ToLower(raw), "jsdelivr.net")
}

func isTimeoutish(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "timeout") || strings.Contains(s, "Timeout")
}

func mirrorLabel(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		if len(raw) > 64 {
			return raw[:64]
		}
		return raw
	}
	return u.Host
}

func FetchAll(ctx context.Context, client *HTTPClient, c Crawler) ([]Proxy, error) {
	seen := map[string]struct{}{}
	out := make([]Proxy, 0, 64)
	var errs []string
	skipJS := jsdelivrBlocked.Load()
	for _, u := range c.URLs() {
		if isJsdelivrURL(u) && skipJS {
			errs = append(errs, mirrorLabel(u)+": skipped (cloudflare/jsdelivr blocked)")
			continue
		}
		wait := 8 * time.Second
		if isJsdelivrURL(u) {
			wait = 2 * time.Second
		}
		uCtx, cancel := context.WithTimeout(ctx, wait)
		body, err := client.Get(uCtx, u)
		cancel()
		if err != nil {
			if isJsdelivrURL(u) && isTimeoutish(err) {
				jsdelivrBlocked.Store(true)
				skipJS = true
			}
			errs = append(errs, mirrorLabel(u)+": "+err.Error())
			continue
		}
		items, err := c.Parse(body, u)
		if err != nil {
			errs = append(errs, mirrorLabel(u)+": "+err.Error())
			continue
		}
		got := 0
		for _, item := range items {
			if item.Protocol == "" {
				item.Protocol = c.Protocol()
			}
			key := item.Host + ":" + strconv.Itoa(item.Port) + ":" + item.Protocol
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, item)
			got++
		}
		// Fallback URLs are mirrors of the same list: stop after the first
		// that actually yields proxies so a dead ghproxy does not block the rest.
		if got > 0 {
			return out, nil
		}
		errs = append(errs, mirrorLabel(u)+": 0 proxies")
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, " | "))
	}
	return out, nil
}

func ParseHTMLTable(body []byte, hostIdx, portIdx int) ([]Proxy, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	var out []Proxy
	doc.Find("table tr").Each(func(i int, sel *goquery.Selection) {
		if i == 0 {
			return
		}
		tds := sel.Find("td")
		if tds.Length() <= hostIdx || tds.Length() <= portIdx {
			return
		}
		host := strings.TrimSpace(tds.Eq(hostIdx).Text())
		portText := strings.TrimSpace(tds.Eq(portIdx).Text())
		port, err := strconv.Atoi(portText)
		if err != nil {
			return
		}
		if h, p, ok := splitHostPort(host + ":" + strconv.Itoa(port)); ok {
			out = append(out, Proxy{Host: h, Port: p, Protocol: "http"})
		}
	})
	if len(out) == 0 {
		// fallback regex
		return ExtractIPPort(string(body)), nil
	}
	return out, nil
}

// Generic crawler helpers

type plainTextCrawler struct {
	name           string
	urls           []string
	protocol       string
	fragile        bool
	defaultEnabled bool
}

func PlainText(name string, urls []string, protocol string, fragile bool, enabled bool) Crawler {
	if protocol == "" {
		protocol = "http"
	}
	return &plainTextCrawler{name: name, urls: urls, protocol: protocol, fragile: fragile, defaultEnabled: enabled}
}

func (c *plainTextCrawler) Name() string         { return c.name }
func (c *plainTextCrawler) URLs() []string       { return c.urls }
func (c *plainTextCrawler) Protocol() string     { return c.protocol }
func (c *plainTextCrawler) Fragile() bool        { return c.fragile }
func (c *plainTextCrawler) DefaultEnabled() bool { return c.defaultEnabled }
func (c *plainTextCrawler) Parse(body []byte, _ string) ([]Proxy, error) {
	items := ExtractIPPort(string(body))
	for i := range items {
		items[i].Protocol = c.protocol
	}
	return items, nil
}

type htmlTableCrawler struct {
	name           string
	urls           []string
	protocol       string
	fragile        bool
	defaultEnabled bool
	hostIdx        int
	portIdx        int
}

func HTMLTable(name string, urls []string, hostIdx, portIdx int, fragile, enabled bool) Crawler {
	return &htmlTableCrawler{
		name: name, urls: urls, protocol: "http", fragile: fragile, defaultEnabled: enabled,
		hostIdx: hostIdx, portIdx: portIdx,
	}
}

func (c *htmlTableCrawler) Name() string         { return c.name }
func (c *htmlTableCrawler) URLs() []string       { return c.urls }
func (c *htmlTableCrawler) Protocol() string     { return c.protocol }
func (c *htmlTableCrawler) Fragile() bool        { return c.fragile }
func (c *htmlTableCrawler) DefaultEnabled() bool { return c.defaultEnabled }
func (c *htmlTableCrawler) Parse(body []byte, _ string) ([]Proxy, error) {
	return ParseHTMLTable(body, c.hostIdx, c.portIdx)
}

type regexCrawler struct {
	name           string
	urls           []string
	protocol       string
	fragile        bool
	defaultEnabled bool
}

func RegexSource(name string, urls []string, protocol string, fragile, enabled bool) Crawler {
	if protocol == "" {
		protocol = "http"
	}
	return &regexCrawler{name: name, urls: urls, protocol: protocol, fragile: fragile, defaultEnabled: enabled}
}

func (c *regexCrawler) Name() string         { return c.name }
func (c *regexCrawler) URLs() []string       { return c.urls }
func (c *regexCrawler) Protocol() string     { return c.protocol }
func (c *regexCrawler) Fragile() bool        { return c.fragile }
func (c *regexCrawler) DefaultEnabled() bool { return c.defaultEnabled }
func (c *regexCrawler) Parse(body []byte, _ string) ([]Proxy, error) {
	items := ExtractIPPort(string(body))
	for i := range items {
		items[i].Protocol = c.protocol
	}
	return items, nil
}
