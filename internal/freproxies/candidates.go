package freproxies

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"

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
	if len(result.Items) == 0 {
		// include raw proxies so they can be selected into pools before validation finishes
		result, err = s.store.List(ctx, ListFilter{Page: 1, Size: limit})
		if err != nil {
			return nil, err
		}
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
	if len(result.Items) == 0 {
		result, err = s.store.List(ctx, ListFilter{Page: 1, Size: limit})
		if err != nil {
			return nil, err
		}
	}
	out := make([]models.RuntimeNode, 0, len(result.Items))
	for _, p := range result.Items {
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
	items, err := s.PickValidatedNFilter(ctx, protocol, region, 1)
	if err != nil {
		return Proxy{}, err
	}
	return items[0], nil
}

// PickValidatedN returns up to n shuffled free proxies for failover dialing.
func (s *Service) PickValidatedN(ctx context.Context, protocol string, n int) ([]Proxy, error) {
	return s.PickValidatedNFilter(ctx, protocol, "", n)
}

func (s *Service) PickValidatedNFilter(ctx context.Context, protocol, region string, n int) ([]Proxy, error) {
	if n <= 0 {
		n = 1
	}
	try := func(items []Proxy) []Proxy {
		return s.filterPick(items, region, n)
	}
	// Hot cache first — zero Redis on hit
	if s.hot != nil {
		if out := try(s.hot.Pick(max(n*3, 8), protocol, region)); len(out) > 0 {
			return out, nil
		}
	}
	if items, err := s.store.RandomN(ctx, protocol, max(n*3, 8)); err == nil {
		if out := try(items); len(out) > 0 {
			return out, nil
		}
	}
	if protocol != "" {
		if items, err := s.store.RandomN(ctx, "", max(n*3, 8)); err == nil {
			if out := try(items); len(out) > 0 {
				return out, nil
			}
		}
	}
	list, err := s.store.List(ctx, ListFilter{Page: 1, Size: max(n*10, 40), OnlyOK: true, Protocol: protocol, Region: region})
	if err != nil || len(list.Items) == 0 {
		list, err = s.store.List(ctx, ListFilter{Page: 1, Size: max(n*10, 40), OnlyOK: true})
	}
	if err != nil || len(list.Items) == 0 {
		list, err = s.store.List(ctx, ListFilter{Page: 1, Size: max(n*10, 40)})
	}
	if err != nil {
		return nil, err
	}
	out := try(list.Items)
	if len(out) == 0 {
		return nil, fmt.Errorf("no free proxy available")
	}
	return out, nil
}

func (s *Service) filterPick(items []Proxy, region string, n int) []Proxy {
	out := make([]Proxy, 0, n)
	for _, p := range items {
		if s.blocked != nil && s.blocked(p.Addr) {
			continue
		}
		if s.sourceDisabled != nil && s.sourceDisabled(p.Source) {
			continue
		}
		if region != "" && p.Region != "" && !strings.EqualFold(p.Region, region) && !strings.Contains(strings.ToLower(p.Region), strings.ToLower(region)) {
			continue
		}
		out = append(out, p)
		if len(out) >= n {
			break
		}
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
