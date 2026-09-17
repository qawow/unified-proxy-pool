package freproxies

import (
	"context"
	"sort"
)

// PoolHealth is the exit pool translated into something an operator can act on.
//
// The raw numbers a person sees on the panel — 25 validated, 3436 raw,
// avg_score 50.3 — answer none of the questions they actually have: is this
// pool usable right now, why is it like this, and should I do anything. This
// type is that translation: a verdict, the latency shape behind it, and the
// reason the verdict is what it is.
type PoolHealth struct {
	// Available is how many validated proxies can be served right now.
	Available int `json:"available"`
	// Status is the machine-readable verdict: healthy / degraded / slow /
	// critical / empty.
	Status string `json:"status"`
	// Verdict is one human sentence, e.g. "可用但整体偏慢".
	Verdict string `json:"verdict"`
	// Latency bands, in ms, counting only validated proxies.
	Fast           int   `json:"fast"`   // < 500ms
	Usable         int   `json:"usable"` // 500-1500ms
	Slow           int   `json:"slow"`   // > 1500ms
	MedianLatency  int64 `json:"median_latency_ms"`
	FastestLatency int64 `json:"fastest_latency_ms"`
	// RawPending is how many addresses have never been tested. It is the
	// distance between "what I have" and "what I could have".
	RawPending int64 `json:"raw_pending"`
	// LastFailReasons is the most recent batch's failure breakdown, so the
	// verdict can be checked against evidence: all-timeout means the network,
	// all-refused means the proxies.
	LastFailReasons map[string]int `json:"last_fail_reasons,omitempty"`
	// Advice is the one thing worth doing, if any.
	Advice string `json:"advice,omitempty"`
}

// latency bands for the health bands. 500ms is a usable web proxy, 1500ms is
// where a person notices every page load.
const (
	bandFast = 500
	bandSlow = 1500

	// A pool needs this many usable proxies before it counts as healthy; below
	// it, one proxy dying is a user-visible outage.
	healthyMinUsable = 5
)

// Health evaluates the exit pool. It reads the validated set plus the current
// validator queues, and never fails: a pool that cannot be read reports empty
// rather than erroring, because the panel needs something to show.
func (s *Service) Health(ctx context.Context) PoolHealth {
	h := PoolHealth{}

	validated, err := s.store.ListValidated(ctx, 5000)
	if err == nil {
		latencies := make([]int64, 0, len(validated))
		for _, p := range validated {
			if !p.Validated {
				continue
			}
			h.Available++
			if p.LatencyMS > 0 {
				latencies = append(latencies, p.LatencyMS)
			}
			switch {
			case p.LatencyMS <= 0:
				// Latency unknown (never re-probed since scoring); treat as
				// usable rather than guessing it is slow.
				h.Usable++
			case p.LatencyMS < bandFast:
				h.Fast++
			case p.LatencyMS <= bandSlow:
				h.Usable++
			default:
				h.Slow++
			}
		}
		if len(latencies) > 0 {
			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			h.FastestLatency = latencies[0]
			h.MedianLatency = latencies[len(latencies)/2]
		}
	}

	if q, err := s.store.Queues(ctx); err == nil {
		h.RawPending = q.RawCount
		h.LastFailReasons = q.LastFailReasons
	}

	h.Status, h.Verdict, h.Advice = evaluateHealth(h)
	return h
}

// evaluateHealth turns the measurements into a verdict. Separate from Health
// so the bands can be unit-tested against synthetic shapes.
func evaluateHealth(h PoolHealth) (status, verdict, advice string) {
	switch {
	case h.Available == 0:
		return "empty",
			"出口池没有可用代理",
			"校验器正在消化待验队列；若持续为空，检查校验 URL 是否可达"
	case h.Available < 3:
		return "critical",
			"可用代理太少，挂一个就是断线",
			"等待校验轮消化待验队列，或手动提交一批地址"
	case h.Fast == 0 && h.Slow >= h.Available/2:
		return "slow",
			"可用，但没有快代理，每个页面都会被拖慢",
			"可用代理都慢；扩大待验队列或放宽地区限制可提高快代理命中率"
	case h.MedianLatency > bandSlow:
		return "slow",
			"可用但整体偏慢，中位延迟超过 1.5 秒",
			"慢代理集中在低端分桶；优先从高分段取节点"
	case h.Usable+h.Fast < healthyMinUsable:
		return "degraded",
			"勉强可用，但余量不足，一次抖动就会变慢",
			"可用代理少于健康阈值，继续积累待验队列"
	case h.Slow > h.Fast+h.Usable:
		return "degraded",
			"可用，但慢代理占多数，体验取决于运气",
			"慢代理占多数；按延迟排序取节点可改善体感"
	default:
		return "healthy",
			"出口池健康",
			""
	}
}
