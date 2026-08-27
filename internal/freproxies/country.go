package freproxies

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"unified-proxy-pool/internal/geoip"
)

func (s *Service) dropBlocked(items []Proxy) (kept []Proxy, blocked int) {
	f := geoip.Active()
	if !f.Enabled {
		return items, 0
	}
	kept = make([]Proxy, 0, len(items))
	for _, p := range items {
		if f.Blocks(p.Region) || f.BlockedNode(p.Host, "") {
			blocked++
			continue
		}
		kept = append(kept, p)
	}
	return kept, blocked
}

// PurgeBlocked deletes every stored proxy whose recorded region is on the
// deny list. Unknown-region rows stay until the next exit-country check.
func (s *Service) PurgeBlocked(ctx context.Context) (int, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	f := geoip.Active()
	if !f.Enabled || len(f.Blocked) == 0 {
		return 0, nil
	}
	removed := 0
	for page := 1; page <= 50; page++ {
		list, err := s.store.List(ctx, ListFilter{Page: page, Size: 5000})
		if err != nil {
			return removed, err
		}
		if len(list.Items) == 0 {
			break
		}
		for _, p := range list.Items {
			if f.Blocks(p.Region) || f.BlockedNode(p.Host, "") {
				if err := s.store.Delete(ctx, p.Addr); err != nil {
					return removed, err
				}
				removed++
			}
		}
		if int64(page*5000) >= list.Total {
			break
		}
	}
	return removed, nil
}

func proxyTransport(p Proxy, timeout time.Duration) *http.Transport {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	proxyURL := &url.URL{Scheme: "http", Host: p.Addr}
	if p.Protocol == "socks5" || p.Protocol == "socks4" {
		proxyURL.Scheme = "socks5"
	}
	return &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout: timeout,
		}).DialContext,
		TLSHandshakeTimeout: timeout,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		DisableKeepAlives:   true,
	}
}

func (s *Service) probeExitCountry(ctx context.Context, p Proxy, timeout time.Duration) string {
	geo := s.geoSvc
	if geo == nil {
		return ""
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r, err := geo.LookupVia(cctx, proxyTransport(p, timeout))
	if err != nil {
		return ""
	}
	return geoip.FormatRegion(r)
}

func countryBlockedError(region string) error {
	return fmt.Errorf("blocked country %s", region)
}
