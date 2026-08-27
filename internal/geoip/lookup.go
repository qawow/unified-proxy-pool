package geoip

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Result struct {
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Query       string `json:"query,omitempty"`
}

type Cache interface {
	GetGeo(ctx context.Context, ip string) (Result, bool)
	SetGeo(ctx context.Context, ip string, r Result) error
}

type memoryCache struct {
	mu   sync.RWMutex
	data map[string]cacheEntry
}

type cacheEntry struct {
	r   Result
	exp time.Time
}

func NewMemoryCache() *memoryCache {
	return &memoryCache{data: map[string]cacheEntry{}}
}

func (c *memoryCache) GetGeo(_ context.Context, ip string) (Result, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.data[ip]
	if !ok || time.Now().After(e.exp) {
		return Result{}, false
	}
	return e.r, true
}

func (c *memoryCache) SetGeo(_ context.Context, ip string, r Result) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[ip] = cacheEntry{r: r, exp: time.Now().Add(7 * 24 * time.Hour)}
	return nil
}

type Service struct {
	cache    Cache
	client   *http.Client
	sem      chan struct{}
	hostURLs []string
	selfURLs []string
}

var defaultHostURLs = []string{
	"http://ip-api.com/json/%s?fields=status,country,countryCode,query,message",
	"http://ipwho.is/%s?fields=success,country,country_code,ip",
}

func New(cache Cache) *Service {
	if cache == nil {
		cache = NewMemoryCache()
	}
	return &Service{
		cache: cache,
		client: &http.Client{
			Timeout: 4 * time.Second,
		},
		sem:      make(chan struct{}, 2), // max 2 concurrent lookups
		hostURLs: append([]string{}, defaultHostURLs...),
		selfURLs: append([]string{}, defaultSelfURLs...),
	}
}

func (s *Service) Lookup(ctx context.Context, ip string) (Result, error) {
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return Result{}, fmt.Errorf("invalid ip")
	}
	if r, ok := s.cache.GetGeo(ctx, ip); ok {
		return r, nil
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	if r, ok := s.cache.GetGeo(ctx, ip); ok {
		return r, nil
	}
	urls := s.hostURLs
	if len(urls) == 0 {
		urls = defaultHostURLs
	}
	var last error
	for _, tmpl := range urls {
		raw := tmpl
		if strings.Contains(tmpl, "%s") {
			raw = fmt.Sprintf(tmpl, ip)
		}
		r, err := fetchGeo(ctx, s.client, raw)
		if err != nil {
			last = err
			continue
		}
		if r.CountryCode == "" {
			r.CountryCode = Normalize(r.Country)
		} else {
			r.CountryCode = Normalize(r.CountryCode)
		}
		if r.CountryCode == "" {
			last = fmt.Errorf("geoip: empty country")
			continue
		}
		_ = s.cache.SetGeo(ctx, ip, r)
		return r, nil
	}
	if last == nil {
		last = fmt.Errorf("geoip: no provider answered")
	}
	return Result{}, last
}

// LookupHost accepts an IP or a hostname (subscription servers are often names).
func (s *Service) LookupHost(ctx context.Context, host string) (Result, error) {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	if ip := net.ParseIP(host); ip != nil {
		return s.Lookup(ctx, ip.String())
	}
	if host == "" {
		return Result{}, fmt.Errorf("empty host")
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		if err == nil {
			err = fmt.Errorf("no addresses")
		}
		return Result{}, err
	}
	return s.Lookup(ctx, addrs[0].IP.String())
}

// FormatRegion returns a compact ISO code when we have one.
func FormatRegion(r Result) string {
	if code := Normalize(r.CountryCode); code != "" {
		return code
	}
	if code := Normalize(r.Country); code != "" {
		return code
	}
	return r.CountryCode
}
