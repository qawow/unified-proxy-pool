package freproxies

import "time"

const (
	ScoreInit = 10
	ScoreMax  = 100
	ScoreMin  = 0
)

type Proxy struct {
	Addr       string    `json:"addr"`
	Host       string    `json:"host"`
	Port       int       `json:"port"`
	Protocol   string    `json:"protocol"`
	Source     string    `json:"source"`
	Score      float64   `json:"score"`
	LatencyMS  int64     `json:"latency_ms"`
	Region     string    `json:"region"`
	Validated  bool      `json:"validated"`
	LastCheck  time.Time `json:"last_check"`
	FailCount  int       `json:"fail_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type ScraperStat struct {
	Name       string    `json:"name"`
	Enabled    bool      `json:"enabled"`
	Protocol   string    `json:"protocol"`
	LastRunAt  time.Time `json:"last_run_at"`
	LastOK     int       `json:"last_ok"`
	LastFail   int       `json:"last_fail"`
	LastError  string    `json:"last_error"`
	TotalOK    int64     `json:"total_ok"`
	TotalFail  int64     `json:"total_fail"`
	URLHint    string    `json:"url_hint"`
	Fragile    bool      `json:"fragile"`
	Builtin    bool      `json:"builtin"`
	Format     string    `json:"format"`
	URLs       []string  `json:"urls,omitempty"`
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
	MinScore float64
	Query    string
	OnlyOK   bool
}

type ListResult struct {
	Items []Proxy `json:"items"`
	Total int64   `json:"total"`
	Page  int     `json:"page"`
	Size  int     `json:"size"`
}

type ValidatorQueues struct {
	RawCount       int64            `json:"raw_count"`
	ValidatedCount int64            `json:"validated_count"`
	ScoreBuckets   map[string]int64 `json:"score_buckets"`
	ProtocolCounts map[string]int64 `json:"protocol_counts"`
	FailTopSources []SourceFail     `json:"fail_top_sources"`
}

type SourceFail struct {
	Name  string `json:"name"`
	Fails int64  `json:"fails"`
}
