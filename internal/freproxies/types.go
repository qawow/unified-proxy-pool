package freproxies

import (
	"net"
	"strings"
	"time"
)

const (
	ScoreInit = 10
	ScoreMax  = 100
	ScoreMin  = 0
)

// Quality scoring.
//
// MarkValidated used to write a flat ScoreMax on every success, so keyScored
// could not rank the pool at all and picks fell back to random sampling within
// it (see the ZRandMember comment in store.go). A 3-second proxy and a
// 100-millisecond proxy were indistinguishable at the score level. These turn
// the latency reading into a continuous quality score.
const (
	// qualityScorePerMS is how much latency costs: 100ms keeps 98, 1s keeps 80,
	// 3s keeps 40.
	qualityScorePerMS = 50
	// qualityFloor keeps a slow-but-working proxy reachable. The per-pick
	// weight already decays with latency, so the floor only has to stop the
	// score from signalling "dead".
	qualityFloor = 20
)

// qualityScore maps a latency reading onto [qualityFloor, ScoreMax]. Negative
// or zero latency (clock skew, an instant cached hit) reads as fully healthy.
func qualityScore(latencyMS int64) float64 {
	if latencyMS <= 0 {
		return ScoreMax
	}
	score := float64(ScoreMax) - float64(latencyMS)/float64(qualityScorePerMS)
	if score < qualityFloor {
		score = qualityFloor
	}
	if score > ScoreMax {
		score = ScoreMax
	}
	return score
}

// smoothLatency damps single-sample jitter with an exponential moving average.
// A proxy that is fast for hours should not plummet in weight because one probe
// hit a slow route, and a proxy recovering from a slow spell should not jump to
// the top on one good reading.
func smoothLatency(prev, next int64) int64 {
	if prev <= 0 {
		return next
	}
	return int64(0.7*float64(prev) + 0.3*float64(next))
}

// blendScore eases a live proxy toward a new quality reading instead of
// jumping straight to it. Same reasoning as smoothLatency, applied to score.
func blendScore(prev, quality float64) float64 {
	return 0.7*prev + 0.3*quality
}

// IP family identifiers used by Proxy.IPFamily and ListFilter.Family.
const (
	FamilyIPv4    = "ipv4"
	FamilyIPv6    = "ipv6"
	FamilyUnknown = "unknown"
)

// DetectFamily classifies a host string as ipv4 / ipv6 / unknown.
// Bracketed IPv6 literals ("[::1]") are accepted; hostnames yield unknown.
func DetectFamily(host string) string {
	h := strings.TrimSpace(host)
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	if h == "" {
		return FamilyUnknown
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return FamilyUnknown
	}
	if ip.To4() != nil {
		return FamilyIPv4
	}
	return FamilyIPv6
}

type Proxy struct {
	Addr      string    `json:"addr"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	Protocol  string    `json:"protocol"`
	Source    string    `json:"source"`
	IPFamily  string    `json:"ip_family"`
	Score     float64   `json:"score"`
	LatencyMS int64     `json:"latency_ms"`
	Region    string    `json:"region"`
	Validated bool      `json:"validated"`
	LastCheck time.Time `json:"last_check"`
	FailCount int       `json:"fail_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Username  string    `json:"username,omitempty"`
	Password  string    `json:"password,omitempty"`
}

// Family returns the stored family, deriving it lazily for records written
// before the ip_family field existed.
func (p Proxy) Family() string {
	if p.IPFamily != "" {
		return p.IPFamily
	}
	return DetectFamily(p.Host)
}

type ScraperStat struct {
	Name         string    `json:"name"`
	Enabled      bool      `json:"enabled"`
	Protocol     string    `json:"protocol"`
	LastRunAt    time.Time `json:"last_run_at"`
	LastOK       int       `json:"last_ok"`
	LastFail     int       `json:"last_fail"`
	LastError    string    `json:"last_error"`
	TotalOK      int64     `json:"total_ok"`
	TotalFail    int64     `json:"total_fail"`
	URLHint      string    `json:"url_hint"`
	Fragile      bool      `json:"fragile"`
	Builtin      bool      `json:"builtin"`
	Format       string    `json:"format"`
	URLs         []string  `json:"urls,omitempty"`
	AutoDisabled bool      `json:"auto_disabled,omitempty"`
}

type Overview struct {
	TotalProxies     int64            `json:"total_proxies"`
	ValidatedProxies int64            `json:"validated_proxies"`
	RawProxies       int64            `json:"raw_proxies"`
	SourceCount      int              `json:"source_count"`
	EnabledSources   int              `json:"enabled_sources"`
	AvgScore         float64          `json:"avg_score"`
	RedisOK          bool             `json:"redis_ok"`
	Backend          string           `json:"backend"`
	RegionTop        []RegionCount    `json:"region_top"`
	RecentEvents     []string         `json:"recent_events"`
	QueueDepth       map[string]int64 `json:"queue_depth"`
	LANIPs           []string         `json:"lan_ips"`
	PanelHint        string           `json:"panel_hint"`
	Traffic          any              `json:"traffic,omitempty"`
	ChannelBans      int              `json:"channel_bans"`
	ChannelCount     int              `json:"channel_count"`
	// PoolHealth is the exit pool as a verdict. The counts above answer "how
	// many"; this answers "is that any good" — which is what a person opening
	// the panel actually wants to know.
	PoolHealth *PoolHealth `json:"pool_health,omitempty"`
}

type RegionCount struct {
	Region string `json:"region"`
	Count  int64  `json:"count"`
}

type ListFilter struct {
	Page     int
	Size     int
	Source   string
	Protocol string
	Region   string
	Family   string // "", ipv4, ipv6, unknown
	Group    string // custom group name; resolved to a GroupRule by the service
	MinScore float64
	Query    string
	OnlyOK   bool

	// groupRule is populated internally by the service when Group is set.
	groupRule *GroupRule
}

// MaxListPage bounds pagination. (Page-1)*Size is computed as an int, so an
// unbounded page number overflows to a negative offset and panics the slice
// expression that follows.
const (
	MaxListPage = 100000
	MaxListSize = 5000
)

// Normalize clamps user-supplied pagination into a range the offset arithmetic
// can represent.
func (f *ListFilter) Normalize() {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.Page > MaxListPage {
		f.Page = MaxListPage
	}
	if f.Size <= 0 {
		f.Size = 20
	}
	if f.Size > MaxListSize {
		f.Size = MaxListSize
	}
}

type ListResult struct {
	Items []Proxy `json:"items"`
	Total int64   `json:"total"`
	Page  int     `json:"page"`
	Size  int     `json:"size"`
	// Truncated reports that matches may exist beyond the scanned window, so
	// Total is a lower bound rather than an exact count. Only the filtered
	// Redis path can set this; the unfiltered path paginates in Redis and the
	// memory store scans everything, so both always report exact totals.
	Truncated bool `json:"truncated,omitempty"`
}

// GroupRule is the matching criteria of a proxy group. An empty slice/zero
// value means "no constraint on this dimension". All non-empty dimensions are
// ANDed together, while values inside one dimension are ORed.
type GroupRule struct {
	Sources   []string `json:"sources,omitempty"`
	Protocols []string `json:"protocols,omitempty"`
	Families  []string `json:"families,omitempty"`
	Regions   []string `json:"regions,omitempty"`
	MinScore  float64  `json:"min_score,omitempty"`
	OnlyOK    bool     `json:"only_ok,omitempty"`
}

// Matches reports whether a proxy satisfies every constrained dimension.
func (r GroupRule) Matches(p Proxy) bool {
	if r.OnlyOK && !p.Validated {
		return false
	}
	if r.MinScore > 0 && p.Score < r.MinScore {
		return false
	}
	if !matchAnyFold(r.Sources, p.Source) {
		return false
	}
	if !matchAnyFold(r.Protocols, p.Protocol) {
		return false
	}
	if !matchAnyFold(r.Families, p.Family()) {
		return false
	}
	if len(r.Regions) > 0 {
		region := strings.ToLower(p.Region)
		hit := false
		for _, want := range r.Regions {
			want = strings.ToLower(strings.TrimSpace(want))
			if want == "" {
				continue
			}
			if strings.Contains(region, want) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// IsEmpty reports whether the rule constrains nothing.
func (r GroupRule) IsEmpty() bool {
	return len(r.Sources) == 0 && len(r.Protocols) == 0 && len(r.Families) == 0 &&
		len(r.Regions) == 0 && r.MinScore <= 0 && !r.OnlyOK
}

func matchAnyFold(want []string, got string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if strings.EqualFold(w, got) {
			return true
		}
	}
	return false
}

// ProxyGroup is a user-defined (or built-in) named view over the proxy pool.
type ProxyGroup struct {
	Name      string    `json:"name"`
	Label     string    `json:"label"`
	Rule      GroupRule `json:"rule"`
	Builtin   bool      `json:"builtin"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProxyGroupView augments a group with live counts.
type ProxyGroupView struct {
	ProxyGroup
	Total     int64 `json:"total"`
	Validated int64 `json:"validated"`
}

// BuiltinGroups are always available and cannot be edited or deleted.
func BuiltinGroups() []ProxyGroup {
	return []ProxyGroup{
		{Name: "ipv4", Label: "IPv4", Builtin: true, Rule: GroupRule{Families: []string{FamilyIPv4}}},
		{Name: "ipv6", Label: "IPv6", Builtin: true, Rule: GroupRule{Families: []string{FamilyIPv6}}},
		{Name: "http", Label: "HTTP", Builtin: true, Rule: GroupRule{Protocols: []string{"http", "https"}}},
		{Name: "socks5", Label: "SOCKS5", Builtin: true, Rule: GroupRule{Protocols: []string{"socks5"}}},
		{Name: "validated", Label: "已验证", Builtin: true, Rule: GroupRule{OnlyOK: true}},
	}
}

// QualityBuckets is the validated pool spread across the quality scale. It is
// the honest health read: a healthy pool clusters in the high buckets, while a
// pool that survives only on slow proxies sags into the low ones. Buckets are
// labelled by score range so the panel can render them without knowing the
// numeric scale.
type QualityBuckets struct {
	Buckets   []ScoreBucket `json:"buckets"`
	Total     int64         `json:"total"`
	AvgScore  float64       `json:"avg_score"`
	UpdatedAt string        `json:"updated_at"`
}

// ScoreBucket is one quality range. High is what the pool wants to be made of.
type ScoreBucket struct {
	Label string  `json:"label"` // "81-100", "61-80", ...
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Count int64   `json:"count"`
}

// qualityBucketEdges partitions [0, ScoreMax] into the ranges the panel shows.
// Five even bands over a 0-100 scale.
var qualityBucketEdges = [][2]float64{
	{0, 20}, {21, 40}, {41, 60}, {61, 80}, {81, 100},
}

// bucketForScore returns the edge range a score falls into.
func bucketForScore(score float64) [2]float64 {
	for _, e := range qualityBucketEdges {
		if score >= e[0] && score <= e[1] {
			return e
		}
	}
	if score < 0 {
		return qualityBucketEdges[0]
	}
	return qualityBucketEdges[len(qualityBucketEdges)-1]
}

type ValidatorQueues struct {
	RawCount         int64            `json:"raw_count"`
	ValidatedCount   int64            `json:"validated_count"`
	RetryCount       int64            `json:"retry_count"`
	ScoreBuckets     map[string]int64 `json:"score_buckets"`
	ProtocolCounts   map[string]int64 `json:"protocol_counts"`
	FamilyCounts     map[string]int64 `json:"family_counts"`
	LatencyBuckets   map[string]int64 `json:"latency_buckets"`
	RegionCounts     []RegionCount    `json:"region_counts"`
	AvgLatencyMS     float64          `json:"avg_latency_ms"`
	FailTopSources   []SourceFail     `json:"fail_top_sources"`
	SourceStats      []SourceStatSnap `json:"source_stats,omitempty"`
	LastBatchOK      int              `json:"last_batch_ok"`
	LastBatchFail    int              `json:"last_batch_fail"`
	LastBatchRaw     int              `json:"last_batch_raw"`
	LastBatchRecheck int              `json:"last_batch_recheck"`
	LastBatchAt      *time.Time       `json:"last_batch_at,omitempty"`
	LastBatchMS      int64            `json:"last_batch_ms"`
	// LastFailReasons breaks a batch's failures down by class. The answer to
	// "500/500 failed, why": all-timeouts is the network or the validate URL,
	// all-refused is dead proxies, all-tls is interception.
	LastFailReasons map[string]int `json:"last_fail_reasons,omitempty"`
	Running         bool           `json:"running"`
	BatchSize       int            `json:"batch_size"`
	BatchDone       int            `json:"batch_done"`
	LifetimeOK      int64          `json:"lifetime_ok"`
	LifetimeFail    int64          `json:"lifetime_fail"`
	LifetimeBatches int64          `json:"lifetime_batches"`
	RawUnchecked    int            `json:"raw_unchecked"`
	RawScanLeft     int            `json:"raw_scan_left"`
	History         []BatchHistory `json:"history,omitempty"`
}

type BatchHistory struct {
	OK         int       `json:"ok"`
	Fail       int       `json:"fail"`
	Raw        int       `json:"raw"`
	Recheck    int       `json:"recheck"`
	DurationMS int64     `json:"duration_ms"`
	At         time.Time `json:"at"`
}

type SourceStatSnap struct {
	Name          string     `json:"name"`
	OK            int64      `json:"ok"`
	Fail          int64      `json:"fail"`
	SuccessRate   float64    `json:"success_rate"`
	AvgLatencyMS  float64    `json:"avg_latency_ms"`
	RecentOK      int64      `json:"recent_ok"`
	RecentFail    int64      `json:"recent_fail"`
	RecentRate    float64    `json:"recent_rate"`
	AutoDisabled  bool       `json:"auto_disabled"`
	DisabledUntil *time.Time `json:"disabled_until,omitempty"`
}

type SourceFail struct {
	Name  string `json:"name"`
	Fails int64  `json:"fails"`
}
