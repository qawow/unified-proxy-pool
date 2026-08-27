package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"unified-proxy-pool/internal/netutil"
)

// Self-lookup URLs (no IP in the path) so a request made *through* a proxy
// returns the exit country websites would see. Pattern from
// monosans/proxy-scraper-checker and ProxyBroker: geolocate the tunnel, not
// the listening host.
var defaultSelfURLs = []string{
	"http://ip-api.com/json?fields=status,country,countryCode,query,message",
	"http://ipwho.is/?fields=success,country,country_code,ip",
}

// LookupVia asks geo endpoints through rt. The RoundTripper is the proxy
// transport; this package never imports freproxies.
func (s *Service) LookupVia(ctx context.Context, rt http.RoundTripper) (Result, error) {
	if s == nil {
		return Result{}, fmt.Errorf("geoip: nil service")
	}
	if rt == nil {
		return Result{}, fmt.Errorf("geoip: nil transport")
	}
	if s.sem != nil {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	client := &http.Client{Transport: rt, Timeout: s.client.Timeout}
	urls := s.selfURLs
	if len(urls) == 0 {
		urls = defaultSelfURLs
	}
	var last error
	for _, raw := range urls {
		r, err := fetchGeo(ctx, client, raw)
		if err != nil {
			last = err
			continue
		}
		if r.CountryCode != "" || r.Country != "" {
			return r, nil
		}
	}
	if last == nil {
		last = fmt.Errorf("geoip: empty via-proxy response")
	}
	return Result{}, last
}

func fetchGeo(ctx context.Context, client *http.Client, rawURL string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{}, err
	}
	netutil.ApplyDefaultHeaders(req.Header)
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("geoip: http %d", resp.StatusCode)
	}
	return ParseExitBody(body)
}

// ParseExitBody accepts ip-api, ipwho.is, and httpbin/ipify shapes.
func ParseExitBody(body []byte) (Result, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return Result{}, fmt.Errorf("geoip: empty body")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		// ipify sometimes returns a bare IP.
		if ip := strings.TrimSpace(trimmed); looksLikeIP(ip) {
			return Result{Query: ip}, nil
		}
		return Result{}, err
	}
	status, _ := m["status"].(string)
	if status != "" && !strings.EqualFold(status, "success") {
		msg, _ := m["message"].(string)
		if msg == "" {
			msg = status
		}
		return Result{}, fmt.Errorf("geoip: %s", msg)
	}
	if ok, exists := m["success"]; exists {
		switch v := ok.(type) {
		case bool:
			if !v {
				return Result{}, fmt.Errorf("geoip: unsuccessful")
			}
		case string:
			if !strings.EqualFold(v, "true") && !strings.EqualFold(v, "success") {
				return Result{}, fmt.Errorf("geoip: unsuccessful")
			}
		}
	}
	r := Result{
		Country:     firstString(m, "country"),
		CountryCode: firstString(m, "countryCode", "country_code", "country_code_iso2"),
		Query:       firstString(m, "query", "ip", "origin"),
	}
	if i := strings.IndexByte(r.Query, ','); i > 0 {
		r.Query = strings.TrimSpace(r.Query[:i])
	}
	if r.CountryCode == "" {
		r.CountryCode = Normalize(r.Country)
	} else {
		r.CountryCode = Normalize(r.CountryCode)
	}
	if r.CountryCode == "" && r.Query == "" {
		return Result{}, fmt.Errorf("geoip: no country in %s", trimmed)
	}
	return r, nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return s
			}
		}
	}
	return ""
}

func looksLikeIP(s string) bool {
	if s == "" {
		return false
	}
	dots := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			dots++
		} else if s[i] == ':' {
			return true // IPv6
		} else if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return dots == 3
}
