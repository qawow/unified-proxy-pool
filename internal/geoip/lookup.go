package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"unified-proxy-pool/internal/netutil"
)

type Result struct {
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
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
	cache  Cache
	client *http.Client
	sem    chan struct{}
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
		sem: make(chan struct{}, 2), // max 2 concurrent lookups
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
	// re-check cache after waiting
	if r, ok := s.cache.GetGeo(ctx, ip); ok {
		return r, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://ip-api.com/json/%s?fields=status,country,countryCode,message", ip), nil)
	if err != nil {
		return Result{}, err
	}
	netutil.ApplyDefaultHeaders(req.Header)
	resp, err := s.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	var body struct {
		Status      string `json:"status"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		Message     string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Result{}, err
	}
	if body.Status != "success" {
		return Result{}, fmt.Errorf("geoip: %s", body.Message)
	}
	r := Result{Country: body.Country, CountryCode: body.CountryCode}
	_ = s.cache.SetGeo(ctx, ip, r)
	return r, nil
}

// FormatRegion returns a compact region label like "US" or "中国".
func FormatRegion(r Result) string {
	if r.CountryCode != "" {
		return r.CountryCode
	}
	return r.Country
}
