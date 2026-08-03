package crawlers

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type Proxy struct {
	Host     string
	Port     int
	Protocol string
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
	hostRe   = regexp.MustCompile(`^(?:\d{1,3}\.){3}\d{1,3}$`)
)

func ExtractIPPort(text string) []Proxy {
	matches := ipPortRe.FindAllString(text, -1)
	seen := map[string]struct{}{}
	out := make([]Proxy, 0, len(matches))
	for _, m := range matches {
		host, port, ok := splitHostPort(m)
		if !ok {
			continue
		}
		key := host + ":" + strconv.Itoa(port)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Proxy{Host: host, Port: port, Protocol: "http"})
	}
	return out
}

func splitHostPort(s string) (string, int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
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
	// basic octet check
	for _, oct := range strings.Split(host, ".") {
		n, _ := strconv.Atoi(oct)
		if n < 0 || n > 255 {
			return "", 0, false
		}
	}
	return host, port, true
}

type HTTPClient struct {
	client *http.Client
}

func NewHTTPClient(timeout time.Duration) *HTTPClient {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &HTTPClient{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
				MaxIdleConns:        32,
				IdleConnTimeout:     30 * time.Second,
				TLSHandshakeTimeout: 8 * time.Second,
			},
		},
	}
}

func (h *HTTPClient) Get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; UnifiedProxyPool/1.0)")
	req.Header.Set("Accept", "text/html,application/json,text/plain,*/*")
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

func FetchAll(ctx context.Context, client *HTTPClient, c Crawler) ([]Proxy, error) {
	seen := map[string]struct{}{}
	out := make([]Proxy, 0, 64)
	var lastErr error
	for _, u := range c.URLs() {
		body, err := client.Get(ctx, u)
		if err != nil {
			lastErr = err
			continue
		}
		items, err := c.Parse(body, u)
		if err != nil {
			lastErr = err
			continue
		}
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
		}
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
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
	name            string
	urls            []string
	protocol        string
	fragile         bool
	defaultEnabled  bool
}

func PlainText(name string, urls []string, protocol string, fragile bool, enabled bool) Crawler {
	if protocol == "" {
		protocol = "http"
	}
	return &plainTextCrawler{name: name, urls: urls, protocol: protocol, fragile: fragile, defaultEnabled: enabled}
}

func (c *plainTextCrawler) Name() string           { return c.name }
func (c *plainTextCrawler) URLs() []string         { return c.urls }
func (c *plainTextCrawler) Protocol() string       { return c.protocol }
func (c *plainTextCrawler) Fragile() bool          { return c.fragile }
func (c *plainTextCrawler) DefaultEnabled() bool   { return c.defaultEnabled }
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
