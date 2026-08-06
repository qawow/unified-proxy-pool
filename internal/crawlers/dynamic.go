package crawlers

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DynamicSpec describes a user-defined scraper source.
type DynamicSpec struct {
	Name       string          `json:"name"`
	URLs       []string        `json:"urls"`
	Format     string          `json:"format"` // plaintext, json, jsonl, html_table, html_regex
	Protocol   string          `json:"protocol"`
	Enabled    bool            `json:"enabled"`
	Fragile    bool            `json:"fragile"`
	HostCol    int             `json:"host_col"`
	PortCol    int             `json:"port_col"`
	OptionsRaw json.RawMessage `json:"parse_options,omitempty"`
}

type dynamicCrawler struct {
	spec DynamicSpec
}

func NewDynamic(spec DynamicSpec) (Crawler, error) {
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return nil, fmt.Errorf("name required")
	}
	if len(spec.URLs) == 0 {
		return nil, fmt.Errorf("at least one url required")
	}
	spec.Format = strings.ToLower(strings.TrimSpace(spec.Format))
	if spec.Format == "" {
		spec.Format = "plaintext"
	}
	switch spec.Format {
	case "plaintext", "json", "jsonl", "html_table", "html_regex", "socks_list":
	default:
		return nil, fmt.Errorf("unsupported format %q", spec.Format)
	}
	if spec.Protocol == "" {
		if spec.Format == "socks_list" {
			spec.Protocol = "socks5"
		} else {
			spec.Protocol = "http"
		}
	}
	if spec.PortCol == 0 && spec.Format == "html_table" && spec.HostCol == 0 {
		spec.PortCol = 1
	}
	return &dynamicCrawler{spec: spec}, nil
}

func (c *dynamicCrawler) Name() string         { return c.spec.Name }
func (c *dynamicCrawler) URLs() []string       { return c.spec.URLs }
func (c *dynamicCrawler) Protocol() string     { return c.spec.Protocol }
func (c *dynamicCrawler) Fragile() bool        { return c.spec.Fragile }
func (c *dynamicCrawler) DefaultEnabled() bool { return c.spec.Enabled }
func (c *dynamicCrawler) Builtin() bool        { return false }
func (c *dynamicCrawler) Format() string       { return c.spec.Format }
func (c *dynamicCrawler) Spec() DynamicSpec    { return c.spec }

func (c *dynamicCrawler) Parse(body []byte, rawURL string) ([]Proxy, error) {
	switch c.spec.Format {
	case "html_table":
		items, err := ParseHTMLTable(body, c.spec.HostCol, c.spec.PortCol)
		for i := range items {
			items[i].Protocol = c.spec.Protocol
		}
		return items, err
	case "json", "jsonl":
		// These formats routed to ExtractIPPort, which needs host and port
		// adjacent in the text — so a user-defined source publishing them as
		// separate fields silently yielded nothing.
		if items := ParseJSONProxies(body, c.spec.Protocol); len(items) > 0 {
			return items, nil
		}
		// Body was not JSON after all; a plain list is the usual alternative.
		items := ExtractIPPort(string(body))
		for i := range items {
			items[i].Protocol = c.spec.Protocol
		}
		return items, nil
	case "html_regex", "plaintext", "socks_list":
		items := ExtractIPPort(string(body))
		for i := range items {
			items[i].Protocol = c.spec.Protocol
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unsupported format")
	}
}

// MetaCrawler optional interface for UI.
type MetaCrawler interface {
	Crawler
	Builtin() bool
	Format() string
}

func IsBuiltin(c Crawler) bool {
	if m, ok := c.(MetaCrawler); ok {
		return m.Builtin()
	}
	return true
}

func CrawlerFormat(c Crawler) string {
	if m, ok := c.(MetaCrawler); ok {
		return m.Format()
	}
	return "builtin"
}

// RegisterDynamic adds/replaces a dynamic crawler.
func (r *Registry) RegisterDynamic(c Crawler) {
	if c == nil {
		return
	}
	name := c.Name()
	if _, exists := r.items[name]; !exists {
		r.order = append(r.order, name)
	}
	r.items[name] = c
}

func (r *Registry) Remove(name string) {
	if _, ok := r.items[name]; !ok {
		return
	}
	delete(r.items, name)
	out := r.order[:0]
	for _, n := range r.order {
		if n != name {
			out = append(out, n)
		}
	}
	r.order = out
}

// Mark builtins with Format builtin via wrapper - plainText etc don't implement MetaCrawler.Builtin true by default via IsBuiltin.
func (c *plainTextCrawler) Builtin() bool  { return true }
func (c *plainTextCrawler) Format() string { return "plaintext" }
func (c *htmlTableCrawler) Builtin() bool  { return true }
func (c *htmlTableCrawler) Format() string { return "html_table" }
func (c *regexCrawler) Builtin() bool      { return true }
func (c *regexCrawler) Format() string     { return "regex" }
