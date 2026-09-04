package freproxies

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"time"

	"unified-proxy-pool/internal/geoip"
	"unified-proxy-pool/internal/models"
)

const SourceTypeFree = "free_proxy"

func NodeID(addr string) int64 {
	// 32-bit id so browser JSON Number stays precise (JS safe int = 2^53-1).
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.TrimSpace(addr)))
	id := int64(h.Sum32())
	if id == 0 {
		id = 1
	}
	return id
}

func (s *Service) ListPoolCandidates(ctx context.Context, limit int) ([]models.PoolMemberView, error) {
	if limit <= 0 {
		limit = 100
	}
	result, err := s.store.List(ctx, ListFilter{Page: 1, Size: limit, OnlyOK: true})
	if err != nil {
		return nil, err
	}
	out := make([]models.PoolMemberView, 0, len(result.Items))
	for _, p := range result.Items {
		lat := p.LatencyMS
		var latPtr *int64
		if lat > 0 {
			latPtr = &lat
		}
		status := "pending"
		if p.Validated {
			status = "available"
		}
		out = append(out, models.PoolMemberView{
			SourceType:    SourceTypeFree,
			SourceNodeID:  NodeID(p.Addr),
			DisplayName:   p.Addr,
			Protocol:      normalizeMihomoProto(p.Protocol),
			Server:        p.Host,
			Port:          p.Port,
			Enabled:       true,
			LastStatus:    status,
			LastLatencyMS: latPtr,
			SourceLabel:   "free:" + p.Source,
		})
	}
	return out, nil
}

func (s *Service) RuntimeNodeByID(ctx context.Context, id int64) (models.RuntimeNode, error) {
	// scan scored then raw
	result, err := s.store.List(ctx, ListFilter{Page: 1, Size: 500})
	if err != nil {
		return models.RuntimeNode{}, err
	}
	for _, p := range result.Items {
		if NodeID(p.Addr) == id {
			return toRuntimeNode(p), nil
		}
	}
	return models.RuntimeNode{}, fmt.Errorf("free proxy id %d not found", id)
}

func (s *Service) AllRuntimeNodes(ctx context.Context, limit int) ([]models.RuntimeNode, error) {
	if limit <= 0 {
		limit = 200
	}
	result, err := s.store.List(ctx, ListFilter{Page: 1, Size: limit, OnlyOK: true})
	if err != nil {
		return nil, err
	}
	out := make([]models.RuntimeNode, 0, len(result.Items))
	f := geoip.Active()
	for _, p := range result.Items {
		if f.Blocks(p.Region) || f.BlockedNode(p.Host, "") {
			continue
		}
		out = append(out, toRuntimeNode(p))
	}
	return out, nil
}

func toRuntimeNode(p Proxy) models.RuntimeNode {
	proto := normalizeMihomoProto(p.Protocol)
	payload := map[string]any{
		"name":   p.Addr,
		"type":   proto,
		"server": p.Host,
		"port":   p.Port,
	}
	if proto == "http" || proto == "https" {
		payload["type"] = "http"
	}
	raw, _ := json.Marshal(payload)
	return models.RuntimeNode{
		SourceType:     SourceTypeFree,
		SourceNodeID:   NodeID(p.Addr),
		DisplayName:    p.Addr,
		Protocol:       payload["type"].(string),
		Server:         p.Host,
		Port:           p.Port,
		RawPayload:     p.Addr,
		NormalizedJSON: string(raw),
		Enabled:        true,
		LastStatus:     map[bool]string{true: "available", false: "pending"}[p.Validated],
	}
}

func normalizeMihomoProto(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "socks5", "socks":
		return "socks5"
	case "socks4":
		// mihomo primarily uses socks5; keep socks5 for outbound if socks4 upstream not ideal
		return "socks5"
	case "https":
		return "http"
	default:
		return "http"
	}
}

// PickValidated returns one validated (or best-effort) free proxy for DirectProxy.
func (s *Service) PickValidated(ctx context.Context, protocol string) (Proxy, error) {
	return s.PickValidatedFilter(ctx, protocol, "")
}

func (s *Service) PickValidatedFilter(ctx context.Context, protocol, region string) (Proxy, error) {
	items, err := s.PickValidatedNFilter(ctx, protocol, region, "", 1)
	if err != nil {
		return Proxy{}, err
	}
	return items[0], nil
}

// PickValidatedN returns up to n shuffled free proxies for failover dialing.
func (s *Service) PickValidatedN(ctx context.Context, protocol string, n int) ([]Proxy, error) {
	return s.PickValidatedNFilter(ctx, protocol, "", "", n)
}

// PickValidatedNFamily restricts the pick to one IP family (ipv4/ipv6).
func (s *Service) PickValidatedNFamily(ctx context.Context, protocol, family string, n int) ([]Proxy, error) {
	return s.PickValidatedNFilter(ctx, protocol, "", family, n)
}

func (s *Service) PickValidatedNFilter(ctx context.Context, protocol, region, family string, n int) ([]Proxy, error) {
	res, err := s.Pick(ctx, PickOptions{Protocol: protocol, Region: region, Family: family, N: n})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// Pick is the single selection entry point. Every other Pick* function is a thin
// wrapper over it.
//
// When opt.Channel is set, proxies temporarily banned for that channel are
// skipped. If honouring the bans would leave nothing to serve, the pick is
// retried without them and PickResult.Relaxed is set — a possibly-banned proxy
// beats a 502.
func (s *Service) Pick(ctx context.Context, opt PickOptions) (PickResult, error) {
	if opt.N <= 0 {
		opt.N = 1
	}
	if opt.Strategy == "" {
		opt.Strategy = s.defaultStrategy
	}

	var banned map[string]time.Time
	if opt.Channel != "" && s.channelPolicy != nil {
		banned = s.channelPolicy.BanSet(opt.Channel)
	}

	items, err := s.gatherCandidates(ctx, opt, banned)
	if len(items) == 0 && len(banned) > 0 {
		// The channel has sidelined everything this filter could reach. Serving
		// nothing is worse than serving a proxy the channel dislikes, so try again
		// ignoring the bans and tell the caller we did.
		if relaxed, rErr := s.gatherCandidates(ctx, opt, nil); len(relaxed) > 0 {
			out := s.applyStrategy(relaxed, opt)
			return PickResult{Items: out, Channel: opt.Channel, Relaxed: true, Strategy: opt.strategy()}, nil
		} else if rErr != nil {
			err = rErr
		}
	}
	if len(items) == 0 {
		if err == nil {
			err = noProxyError(opt.Family)
		}
		return PickResult{Channel: opt.Channel, Strategy: opt.strategy()}, err
	}
	out := s.applyStrategy(items, opt)
	return PickResult{Items: out, Channel: opt.Channel, Strategy: opt.strategy()}, nil
}

func noProxyError(family string) error {
	if family != "" {
		return fmt.Errorf("no free proxy available for family %s", family)
	}
	return fmt.Errorf("no free proxy available")
}

// gatherCandidates walks the cheap sources first and only descends to broader
// queries when the current rung yields nothing usable.
//
// It returns the whole filtered window rather than the first n matches: weighted
// sampling needs a population to weigh, and truncating here would reduce the
// strategy to "take whatever came back first".
func (s *Service) gatherCandidates(ctx context.Context, opt PickOptions, banned map[string]time.Time) ([]Proxy, error) {
	// Wide enough that weights meaningfully differ; a window of 8 makes weighted
	// and random selection nearly indistinguishable.
	window := max(opt.N*8, 64)
	try := func(items []Proxy) []Proxy {
		return s.filterCandidates(items, opt, banned)
	}
	// Hot cache first — zero Redis on hit
	if s.hot != nil {
		if out := try(s.hot.Pick(window, opt.Protocol, opt.Region)); len(out) > 0 {
			return out, nil
		}
	}
	if items, err := s.store.RandomN(ctx, opt.Protocol, window); err == nil {
		if out := try(items); len(out) > 0 {
			return out, nil
		}
	}
	if opt.Protocol != "" {
		// Protocol relaxed deliberately, and only here: filterCandidates enforces
		// it on the rungs above so a socks5 request is not quietly answered with an
		// http proxy while real socks5 proxies were still reachable.
		relaxed := opt
		relaxed.Protocol = ""
		if items, err := s.store.RandomN(ctx, "", window); err == nil {
			if out := s.filterCandidates(items, relaxed, banned); len(out) > 0 {
				return out, nil
			}
		}
	}
	protocol, region, family, n := opt.Protocol, opt.Region, opt.Family, opt.N
	// Progressively relax the query: drop protocol/region, then accept
	// unvalidated proxies. Family is never relaxed — handing an IPv4 proxy to a
	// caller that asked for IPv6 would break it silently. It is pushed down into
	// the store so the hydrate window is spent on candidates that can actually
	// match (IPv6 is rare in most pools).
	size := max(n*10, 40)
	ladder := []ListFilter{
		{Page: 1, Size: size, OnlyOK: true, Protocol: protocol, Region: region, Family: family},
		{Page: 1, Size: size, OnlyOK: true, Family: family},
	}
	var lastErr error
	served := false
	issued := make([]ListFilter, 0, len(ladder))
	for _, f := range ladder {
		// Levels collapse onto one another when the caller passed no protocol
		// and no region, which is the common case for family-only picks.
		// Re-issuing an identical query buys nothing and costs another hydrate
		// window (up to 800 metas on the filtered Redis path).
		if slices.Contains(issued, f) {
			continue
		}
		issued = append(issued, f)
		list, err := s.store.List(ctx, f)
		if err != nil {
			lastErr = err
			continue
		}
		served = true
		// The in-process filter has to match the rung that was actually queried:
		// rungs below the first drop protocol and region on purpose, so enforcing
		// the caller's original values here would reject every row they returned.
		rung := opt
		rung.Protocol = f.Protocol
		rung.Region = f.Region
		// try() also drops blacklisted and disabled-source proxies, which the
		// store filter knows nothing about, so a non-empty page can still yield
		// nothing pickable — keep descending in that case.
		if out := s.filterCandidates(list.Items, rung, banned); len(out) > 0 {
			return out, nil
		}
	}
	if !served && lastErr != nil {
		return nil, lastErr
	}
	return nil, nil
}

// filterCandidates drops everything the caller cannot use. It returns the full
// surviving set, not the first N: the selection strategy needs the population.
//
// This is the single choke point for exclusions — global blacklist, disabled
// source, and per-channel ban all meet here, so no selection path can bypass one
// of them.
func (s *Service) filterCandidates(items []Proxy, opt PickOptions, banned map[string]time.Time) []Proxy {
	out := make([]Proxy, 0, len(items))
	country := geoip.Active()
	for _, p := range items {
		if s.blocked != nil && s.blocked(p.Addr) {
			continue
		}
		if country.Blocks(p.Region) || country.BlockedNode(p.Host, "") {
			continue
		}
		if s.sourceDisabled != nil && s.sourceDisabled(p.Source) {
			continue
		}
		if _, ok := banned[p.Addr]; ok {
			continue
		}
		// Protocol is enforced here as well as in the store query: the hot cache
		// falls back to its unfiltered snapshot when a protocol matches nothing,
		// so without this check a socks5 request could be answered with an http
		// proxy. gatherCandidates relaxes it explicitly when it means to.
		if opt.Protocol != "" && !strings.EqualFold(p.Protocol, opt.Protocol) {
			continue
		}
		if opt.Region != "" && p.Region != "" && !strings.EqualFold(p.Region, opt.Region) && !strings.Contains(strings.ToLower(p.Region), strings.ToLower(opt.Region)) {
			continue
		}
		if opt.Family != "" && !strings.EqualFold(p.Family(), opt.Family) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
