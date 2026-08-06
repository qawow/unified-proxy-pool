export type ApiResponse<T> = {
  success: boolean;
  data?: T;
  message?: string;
};

export type Settings = {
  id: number;
  panel_host: string;
  panel_port: number;
  speed_test_enabled: boolean;
  latency_test_url: string;
  speed_test_url: string;
  latency_timeout_ms: number;
  speed_timeout_ms: number;
  latency_concurrency: number;
  speed_concurrency: number;
  default_subscription_interval_sec: number;
  mihomo_controller_secret: string;
  failure_retry_count: number;
  log_level: string;
  speed_max_bytes: number;
  session_max_age_sec?: number;
  scrape_interval_sec?: number;
  validate_interval_sec?: number;
  free_validate_url?: string;
  free_validate_timeout_ms?: number;
  free_validate_concurrency?: number;
  proxy_chain_hops?: number;
  feature_json?: string;
  feature?: {
    dashboard_cards?: Record<string, boolean>;
    direct_sticky_enabled?: boolean;
    sticky_ttl_sec?: number;
    rate_limit_bytes_per_sec?: number;
    free_validate_urls?: string[];
    source_auto_disable_rate?: number;
    source_min_samples?: number;
    direct_auth_required?: boolean;
    allowed_cidrs?: string[];
    webhook_url?: string;
    webhook_events?: string[];
    alert_validated_min?: number;
    traffic_sample_sec?: number;
    traffic_retain_hours?: number;
    [key: string]: unknown;
  };
};

export type Subscription = {
  id: number;
  name: string;
  url: string;
  headers_json: string;
  enabled: boolean;
  sync_interval_sec: number;
  last_sync_at?: string | null;
  last_sync_status: string;
  last_error: string;
  total_nodes?: number;
  available_nodes?: number;
  invalid_nodes?: number;
  average_latency_ms?: number | null;
};

export type SubscriptionNode = {
  id: number;
  subscription_id: number;
  display_name: string;
  protocol: string;
  server: string;
  port: number;
  enabled: boolean;
  last_latency_ms?: number | null;
  last_speed_mbps?: number | null;
  last_status: string;
  last_test_at?: string | null;
  last_error: string;
};

export type ManualNode = {
  id: number;
  display_name: string;
  protocol: string;
  server: string;
  port: number;
  raw_payload: string;
  enabled: boolean;
  last_latency_ms?: number | null;
  last_speed_mbps?: number | null;
  last_status: string;
  last_test_at?: string | null;
  last_error: string;
};

export type StrategyAdvanced = {
  display_name?: string;
  template?: string;
  group_type?: string;
  lb_strategy?: string;
  health_url?: string;
  interval?: number;
  tolerance?: number;
  lazy?: boolean;
  disable_health?: boolean;
  extra?: Record<string, unknown>;
};

export type StrategyTemplate = {
  id: string;
  name: string;
  description: string;
  defaults?: StrategyAdvanced;
};

export type ProxyPool = {
  id: number;
  name: string;
  auth_username: string;
  auth_password_secret?: string;
  strategy: string;
  strategy_label?: string;
  strategy_advanced_json?: string;
  failover_enabled: boolean;
  enabled: boolean;
  last_published_at?: string | null;
  last_publish_status: string;
  last_error: string;
  current_member_count: number;
  current_healthy_count: number;
};

export type PoolMemberView = {
  source_type: string;
  source_node_id: number;
  display_name: string;
  protocol: string;
  server: string;
  port: number;
  enabled: boolean;
  last_latency_ms?: number | null;
  last_status: string;
  source_label?: string;
};

export type PoolMember = {
  id: number;
  pool_id: number;
  source_type: string;
  source_node_id: number;
  enabled: boolean;
  weight: number;
};

export type MihomoStatus = {
  running?: boolean;
  version?: string;
  binary_path?: string;
  [key: string]: unknown;
};

export type IPFamily = "ipv4" | "ipv6" | "unknown";

export type FreeProxy = {
  addr: string;
  host: string;
  port: number;
  protocol: string;
  source: string;
  ip_family?: IPFamily;
  score: number;
  latency_ms: number;
  region: string;
  validated: boolean;
  last_check?: string;
  fail_count?: number;
};

export type FreeProxyListResult = {
  items: FreeProxy[];
  total: number;
  page: number;
  size: number;
  /** Matches may exist beyond the scanned window, so `total` is a lower bound. */
  truncated?: boolean;
};

/** Matching criteria of a proxy group. Empty dimension = no constraint. */
export type ProxyGroupRule = {
  sources?: string[];
  protocols?: string[];
  families?: IPFamily[];
  regions?: string[];
  min_score?: number;
  only_ok?: boolean;
};

export type ProxyGroup = {
  name: string;
  label: string;
  rule: ProxyGroupRule;
  builtin: boolean;
  created_at?: string;
  updated_at?: string;
};

export type ProxyGroupView = ProxyGroup & {
  total: number;
  validated: number;
};

export type Scraper = {
  name: string;
  enabled: boolean;
  protocol: string;
  last_run_at?: string;
  last_ok: number;
  last_fail: number;
  last_error: string;
  total_ok: number;
  total_fail: number;
  url_hint: string;
  fragile: boolean;
  builtin?: boolean;
  format?: string;
  urls?: string[];
};

export type ValidatorQueues = {
  raw_count: number;
  validated_count: number;
  score_buckets: Record<string, number>;
  protocol_counts: Record<string, number>;
  fail_top_sources: { name: string; fails: number }[];
};

export type TrafficSnapshot = {
  up_bytes: number;
  down_bytes: number;
  connections: number;
  success: number;
  fail: number;
  active_conns: number;
  active_in?: number;
  active_out?: number;
  by_channel?: Record<
    string,
    {
      up_bytes: number;
      down_bytes: number;
      connections: number;
      success: number;
      fail: number;
      active_in?: number;
      active_out?: number;
    }
  >;
  updated_at?: string;
};

export type Overview = {
  total_proxies: number;
  validated_proxies: number;
  raw_proxies: number;
  source_count: number;
  enabled_sources: number;
  avg_score: number;
  redis_ok: boolean;
  backend: string;
  region_top: { region: string; count: number }[];
  recent_events: string[];
  queue_depth: Record<string, number>;
  lan_ips?: string[];
  panel_hint?: string;
  traffic?: TrafficSnapshot;
};

export type DirectProxyStatus = {
  enabled: boolean;
  running: boolean;
  listen_addr: string;
  lan_ips?: string[];
  client_host?: string;
  client_http?: string;
  client_socks5?: string;
  client_examples?: Record<string, string>;
  username?: string;
  requests: number;
  success: number;
  failures: number;
  chain_enabled?: boolean;
  chain_running?: boolean;
  chain_listen_addr?: string;
  chain_hops?: number;
  chain_http?: string;
  chain_socks5?: string;
  chain_examples?: Record<string, string>;
  chain_requests?: number;
  chain_success?: number;
  chain_failures?: number;
  chain_desc?: string;
  chain_path?: string;
  chain_label?: string;
  chain_options?: ChainOptions;
};

export type ChainOptions = {
  enabled?: boolean;
  listen_addr?: string;
  hops?: number;
  failover_tries?: number;
  dial_timeout_ms?: number;
  hop_timeout_ms?: number;
  prefer_distinct_host?: boolean;
  prefer_distinct_region?: boolean;
  entry_proto?: string;
  exit_proto?: string;
  entry_region?: string;
  exit_region?: string;
  sticky_enabled?: boolean;
  sticky_ttl_sec?: number;
  auth_required?: boolean;
  username?: string;
  password?: string;
  allowed_cidrs?: string[];
  rate_limit_bps?: number;
  max_parallel_dial?: number;
};
