package models

import "time"

type Settings struct {
	ID                             int64     `json:"id"`
	PanelHost                      string    `json:"panel_host"`
	PanelPort                      int       `json:"panel_port"`
	PasswordHash                   string    `json:"-"`
	SpeedTestEnabled               bool      `json:"speed_test_enabled"`
	LatencyTestURL                 string    `json:"latency_test_url"`
	SpeedTestURL                   string    `json:"speed_test_url"`
	LatencyTimeoutMS               int       `json:"latency_timeout_ms"`
	SpeedTimeoutMS                 int       `json:"speed_timeout_ms"`
	LatencyConcurrency             int       `json:"latency_concurrency"`
	SpeedConcurrency               int       `json:"speed_concurrency"`
	DefaultSubscriptionIntervalSec int       `json:"default_subscription_interval_sec"`
	MihomoControllerSecret         string    `json:"mihomo_controller_secret"`
	FailureRetryCount              int       `json:"failure_retry_count"`
	LogLevel                       string    `json:"log_level"`
	SpeedMaxBytes                  int64     `json:"speed_max_bytes"`
	// Free proxy / session runtime (persisted, hot-applied where possible)
	SessionMaxAgeSec        int    `json:"session_max_age_sec"`
	ScrapeIntervalSec       int    `json:"scrape_interval_sec"`
	ValidateIntervalSec     int    `json:"validate_interval_sec"`
	FreeValidateURL         string `json:"free_validate_url"`
	FreeValidateTimeoutMS   int    `json:"free_validate_timeout_ms"`
	FreeValidateConcurrency int    `json:"free_validate_concurrency"`
	ProxyChainHops          int    `json:"proxy_chain_hops"`
	// F1–F6 feature bag (JSON). Also exposed as structured Feature on Get.
	FeatureJSON string         `json:"feature_json"`
	Feature     map[string]any `json:"feature,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type Subscription struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	URL             string     `json:"url"`
	HeadersJSON     string     `json:"headers_json"`
	FetchProxy      string     `json:"fetch_proxy"`
	Enabled         bool       `json:"enabled"`
	SyncIntervalSec int        `json:"sync_interval_sec"`
	LastSyncAt      *time.Time `json:"last_sync_at"`
	LastSyncStatus  string     `json:"last_sync_status"`
	LastError       string     `json:"last_error"`
	ETag            string     `json:"etag"`
	LastModified    string     `json:"last_modified"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type SubscriptionListItem struct {
	Subscription
	TotalNodes       int    `json:"total_nodes"`
	AvailableNodes   int    `json:"available_nodes"`
	InvalidNodes     int    `json:"invalid_nodes"`
	AverageLatencyMS *int64 `json:"average_latency_ms"`
}

type SubscriptionNode struct {
	ID             int64      `json:"id"`
	SubscriptionID int64      `json:"subscription_id"`
	DisplayName    string     `json:"display_name"`
	Protocol       string     `json:"protocol"`
	Server         string     `json:"server"`
	Port           int        `json:"port"`
	RawPayload     string     `json:"raw_payload"`
	NormalizedJSON string     `json:"normalized_json"`
	Enabled        bool       `json:"enabled"`
	LastLatencyMS  *int64     `json:"last_latency_ms"`
	LastSpeedMbps  *float64   `json:"last_speed_mbps"`
	LastStatus     string     `json:"last_status"`
	LastTestAt     *time.Time `json:"last_test_at"`
	LastSpeedAt    *time.Time `json:"last_speed_at"`
	LastError      string     `json:"last_error"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type ManualNode struct {
	ID             int64      `json:"id"`
	DisplayName    string     `json:"display_name"`
	Protocol       string     `json:"protocol"`
	Server         string     `json:"server"`
	Port           int        `json:"port"`
	RawPayload     string     `json:"raw_payload"`
	NormalizedJSON string     `json:"normalized_json"`
	Enabled        bool       `json:"enabled"`
	LastLatencyMS  *int64     `json:"last_latency_ms"`
	LastSpeedMbps  *float64   `json:"last_speed_mbps"`
	LastStatus     string     `json:"last_status"`
	LastTestAt     *time.Time `json:"last_test_at"`
	LastSpeedAt    *time.Time `json:"last_speed_at"`
	LastError      string     `json:"last_error"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type ProxyPool struct {
	ID                  int64      `json:"id"`
	Name                string     `json:"name"`
	AuthUsername        string     `json:"auth_username"`
	AuthPasswordSecret  string     `json:"auth_password_secret,omitempty"`
	Strategy            string     `json:"strategy"`
	StrategyLabel       string     `json:"strategy_label"`
	StrategyAdvancedJSON string    `json:"strategy_advanced_json"`
	FailoverEnabled     bool       `json:"failover_enabled"`
	Enabled             bool       `json:"enabled"`
	Channel             string     `json:"channel,omitempty"` // bound dest channel; empty = no filter
	LastPublishedAt     *time.Time `json:"last_published_at"`
	LastPublishStatus   string     `json:"last_publish_status"`
	LastError           string     `json:"last_error"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	CurrentMemberCount  int        `json:"current_member_count"`
	CurrentHealthyCount int        `json:"current_healthy_count"`
}

// StrategyAdvanced is optional per-pool scheduling customization.
// Base Strategy still selects the built-in mode; advanced fields refine Mihomo group.
type StrategyAdvanced struct {
	DisplayName   string         `json:"display_name,omitempty"`
	Template      string         `json:"template,omitempty"` // "", fast_test, stable, hash_sticky, manual_select, custom
	GroupType     string         `json:"group_type,omitempty"`
	LBStrategy    string         `json:"lb_strategy,omitempty"`
	HealthURL     string         `json:"health_url,omitempty"`
	Interval      int            `json:"interval,omitempty"`
	Tolerance     int            `json:"tolerance,omitempty"`
	Lazy          *bool          `json:"lazy,omitempty"`
	DisableHealth bool           `json:"disable_health,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}

type ProxyPoolMember struct {
	ID           int64     `json:"id"`
	PoolID       int64     `json:"pool_id"`
	SourceType   string    `json:"source_type"`
	SourceNodeID int64     `json:"source_node_id"`
	Enabled      bool      `json:"enabled"`
	Weight       int       `json:"weight"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type RuntimeNode struct {
	SourceType     string `json:"source_type"`
	SourceNodeID   int64  `json:"source_node_id"`
	DisplayName    string `json:"display_name"`
	Protocol       string `json:"protocol"`
	Server         string `json:"server"`
	Port           int    `json:"port"`
	RawPayload     string `json:"raw_payload"`
	NormalizedJSON string `json:"normalized_json"`
	Enabled        bool   `json:"enabled"`
	LastStatus     string `json:"last_status"`
}

type PoolMemberView struct {
	SourceType    string   `json:"source_type"`
	SourceNodeID  int64    `json:"source_node_id"`
	DisplayName   string   `json:"display_name"`
	Protocol      string   `json:"protocol"`
	Server        string   `json:"server"`
	Port          int      `json:"port"`
	Enabled       bool     `json:"enabled"`
	LastStatus    string   `json:"last_status"`
	LastLatencyMS *int64   `json:"last_latency_ms"`
	LastSpeedMbps *float64 `json:"last_speed_mbps"`
	SourceLabel   string   `json:"source_label"`
}
