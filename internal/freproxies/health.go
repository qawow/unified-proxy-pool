package freproxies

import (
	"context"
	"sort"
	"time"
)

// PoolHealth describes validation results, not a guarantee of client throughput.
// Live batch failure reasons are attached by the web layer.
type PoolHealth struct {
	Available       int            `json:"available"`
	Status          string         `json:"status"` // healthy/degraded/slow/critical/empty/unknown
	Verdict         string         `json:"verdict"`
	Fast            int            `json:"fast"`   // < 500ms
	Usable          int            `json:"usable"` // 500-1500ms
	Slow            int            `json:"slow"`   // > 1500ms
	UnknownLatency  int            `json:"unknown_latency"`
	MedianLatency   int64          `json:"median_latency_ms"`
	FastestLatency  int64          `json:"fastest_latency_ms"`
	RawPending      int64          `json:"raw_pending"` // addresses awaiting validation
	LastFailReasons map[string]int `json:"last_fail_reasons,omitempty"`
	Advice          string         `json:"advice,omitempty"`
}

const (
	bandFast         = 500
	bandSlow         = 1500
	healthyMinUsable = 5
	healthTTL        = 3 * time.Second
)

// Health caches empty and unavailable snapshots as well as populated pools.
func (s *Service) Health(ctx context.Context) PoolHealth {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	if !s.healthAt.IsZero() && time.Since(s.healthAt) < healthTTL {
		return s.healthCache
	}
	h := s.healthUncached(ctx)
	// A cancelled request must not replace a useful cached snapshot.
	if ctx.Err() == nil {
		s.healthCache = h
		s.healthAt = time.Now()
	}
	return h
}

func unavailableHealth() PoolHealth {
	return PoolHealth{
		Status:  "unknown",
		Verdict: "暂时无法读取出口池状态",
		Advice:  "检查存储服务连接后重试；当前无法确认可用数量",
	}
}

func (s *Service) healthUncached(ctx context.Context) PoolHealth {
	validated, err := s.store.ListValidated(ctx, 5000)
	if err != nil {
		return unavailableHealth()
	}
	q, err := s.store.Queues(ctx)
	if err != nil {
		return unavailableHealth()
	}
	h := PoolHealth{RawPending: q.RawCount}
	latencies := make([]int64, 0, len(validated))
	for _, p := range validated {
		if !p.Validated {
			continue
		}
		h.Available++
		switch {
		case p.LatencyMS <= 0:
			h.UnknownLatency++
		case p.LatencyMS < bandFast:
			h.Fast++
		case p.LatencyMS <= bandSlow:
			h.Usable++
		default:
			h.Slow++
		}
		if p.LatencyMS > 0 {
			latencies = append(latencies, p.LatencyMS)
		}
	}
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		h.FastestLatency = latencies[0]
		mid := len(latencies) / 2
		h.MedianLatency = latencies[mid]
		if len(latencies)%2 == 0 {
			h.MedianLatency = latencies[mid-1] + (latencies[mid]-latencies[mid-1])/2
		}
	}
	h.Status, h.Verdict, h.Advice = evaluateHealth(h)
	return h
}

func evaluateHealth(h PoolHealth) (status, verdict, advice string) {
	switch {
	case h.Available == 0:
		advice = "添加采集源或导入节点，再查看校验结果"
		if h.RawPending > 0 {
			advice = "查看校验进度和失败原因；持续无可用代理时检查校验目标与网络"
		}
		return "empty", "出口池暂时没有校验通过的代理", advice
	case h.Available < 3:
		return "critical", "可用代理较少，备用余量不足", "补充来源或导入稳定节点，提高故障切换余量"
	case h.UnknownLatency == h.Available:
		return "unknown", "代理已通过校验，延迟数据尚未齐全", "等待复检后再比较速度"
	case h.MedianLatency > bandSlow || (h.Fast == 0 && h.Slow*2 >= h.Available):
		return "slow", "可用，但校验延迟偏高", "优先使用低延迟节点，并用实际访问确认体验"
	case h.Usable+h.Fast < healthyMinUsable:
		return "degraded", "低延迟代理较少，备用余量不足", "补充稳定来源或等待复检，观察低延迟节点数量"
	case h.Slow > h.Fast+h.Usable || h.UnknownLatency > 0:
		return "degraded", "可用，部分节点偏慢或延迟待测", "查看节点校验结果，优先选择延迟稳定的节点"
	default:
		return "healthy", "出口池校验状态良好", ""
	}
}
