import type {
  ApiResponse,
  Channel,
  ChannelBan,
  ChannelLog,
  ChannelAllow,
  ChannelRule,
  FreeProxy,
  FreeProxyListResult,
  ManualNode,
  MihomoStatus,
  Overview,
  PoolMember,
  PoolMemberView,
  ProxyGroup,
  ProxyGroupRule,
  ProxyGroupView,
  ProxyPool,
  Scraper,
  Settings,
  Subscription,
  SubscriptionNode,
  ValidatorQueues,
  DirectProxyStatus,
} from "./types";

export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

export async function api<T = unknown>(path: string, options: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string> | undefined),
  };
  if (options.body && !headers["Content-Type"]) {
    headers["Content-Type"] = "application/json";
  }

  const response = await fetch(path, {
    credentials: "same-origin",
    ...options,
    headers,
  });

  const payload = (await response.json().catch(() => ({}))) as ApiResponse<T>;
  if (!response.ok || payload.success === false) {
    const status = response.status || 500;
    throw new ApiError(payload.message || `request failed: ${status}`, status);
  }
  return payload.data as T;
}

function asArray<T>(data: T[] | null | undefined): T[] {
  return Array.isArray(data) ? data : [];
}

/** Typed API surface used by pages — keeps paths/params consistent. */
export const endpoints = {
  auth: {
    login: (password: string) => api("/api/auth/login", { method: "POST", body: JSON.stringify({ password }) }),
    logout: () => api("/api/auth/logout", { method: "POST" }),
    me: () => api("/api/auth/me"),
    changePassword: (old_password: string, new_password: string) =>
      api("/api/auth/change-password", { method: "POST", body: JSON.stringify({ old_password, new_password }) }),
  },
  overview: () => api<Overview>("/api/overview"),
  health: () => api("/api/health"),
  directProxy: {
    status: () => api<DirectProxyStatus>("/api/direct-proxy/status"),
    setChainHops: (hops: number) =>
      api<DirectProxyStatus>("/api/direct-proxy/chain", {
        method: "PUT",
        body: JSON.stringify({ hops }),
      }),
    updateChain: (body: import("./types").ChainOptions) =>
      api<DirectProxyStatus>("/api/direct-proxy/chain", {
        method: "PUT",
        body: JSON.stringify(body),
      }),
  },
  proxies: {
    list: (params: Record<string, string | number | boolean | undefined>) => {
      const q = new URLSearchParams();
      Object.entries(params).forEach(([k, v]) => {
        if (v === undefined || v === "" || v === false) return;
        if (v === true) {
          q.set(k, "1");
          return;
        }
        q.set(k, String(v));
      });
      return api<FreeProxyListResult>(`/api/proxies?${q.toString()}`);
    },
    count: () => api<{ total: number; validated: number; raw: number; count?: number }>("/api/proxies/count"),
    random: (proto?: string) =>
      api<FreeProxy>(`/api/proxies/random${proto ? `?proto=${encodeURIComponent(proto)}` : ""}`),
    test: (addr: string) =>
      api<{ ok: boolean; error?: string; proxy?: FreeProxy }>("/api/proxies/test", {
        method: "POST",
        body: JSON.stringify({ addr }),
      }),
    remove: (addr: string) =>
      api(`/api/proxies?proxy=${encodeURIComponent(addr)}`, { method: "DELETE" }),
    exportUrl: (params: Record<string, string | number | boolean | undefined>) => {
      const q = new URLSearchParams();
      Object.entries(params).forEach(([k, v]) => {
        if (v === undefined || v === "" || v === false) return;
        if (v === true) {
          q.set(k, "1");
          return;
        }
        q.set(k, String(v));
      });
      return `/api/proxies/export?${q.toString()}`;
    },
    purge: (body: Record<string, unknown>) =>
      api<{ matched: number; deleted: number; dry_run: boolean; sample: string[] }>("/api/proxies/purge", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    groups: {
      list: async () => asArray(await api<ProxyGroupView[]>("/api/proxies/groups")),
      save: (body: { name: string; label?: string } & ProxyGroupRule) =>
        api<ProxyGroup>("/api/proxies/groups", {
          method: "POST",
          body: JSON.stringify(body),
        }),
      update: (name: string, body: { label?: string } & ProxyGroupRule) =>
        api<ProxyGroup>(`/api/proxies/groups/${encodeURIComponent(name)}`, {
          method: "PUT",
          body: JSON.stringify(body),
        }),
      remove: (name: string) =>
        api<{ deleted: string }>(`/api/proxies/groups/${encodeURIComponent(name)}`, {
          method: "DELETE",
        }),
    },
  },
  scrapers: {
    list: async () => asArray(await api<Scraper[]>("/api/scrapers")),
    create: (body: Record<string, unknown>) =>
      api("/api/scrapers", { method: "POST", body: JSON.stringify(body) }),
    update: (name: string, body: Record<string, unknown>) =>
      api(`/api/scrapers/${encodeURIComponent(name)}`, { method: "PUT", body: JSON.stringify(body) }),
    remove: (name: string) =>
      api(`/api/scrapers/${encodeURIComponent(name)}`, { method: "DELETE" }),
    toggle: (name: string) => api(`/api/scrapers/${encodeURIComponent(name)}/toggle`, { method: "POST" }),
    run: (name: string) => api(`/api/scrapers/${encodeURIComponent(name)}/run`, { method: "POST" }),
    runAll: () => api("/api/scrapers/run-all", { method: "POST" }),
  },
  validator: {
    queues: () => api<ValidatorQueues>("/api/validator/queues"),
    run: () => api("/api/validator/run", { method: "POST" }),
    logs: (limit = 100) =>
      api<{ items: { at: string; level: string; addr?: string; message: string; latency_ms?: number; source?: string }[]; running: boolean }>(
        `/api/validator/logs?limit=${limit}`,
      ),
    clearLogs: () => api("/api/validator/logs/clear", { method: "POST" }),
    reenable: (name: string) =>
      api(`/api/validator/sources/${encodeURIComponent(name)}/reenable`, { method: "POST" }),
  },
  blacklist: {
    list: async () => asArray(await api<{ host: string; reason: string; until?: string; created_at: string }[]>("/api/blacklist")),
    add: (body: { host?: string; addr?: string; reason?: string; ttl_sec?: number }) =>
      api("/api/blacklist", { method: "POST", body: JSON.stringify(body) }),
    remove: (host: string) => api(`/api/blacklist?host=${encodeURIComponent(host)}`, { method: "DELETE" }),
  },
  channels: {
    list: async (params?: { q?: string; onlyBanned?: boolean }) => {
      const qs = new URLSearchParams();
      if (params?.q) qs.set("q", params.q);
      if (params?.onlyBanned) qs.set("only_banned", "1");
      const suffix = qs.toString();
      return asArray(await api<Channel[]>(`/api/channels${suffix ? `?${suffix}` : ""}`));
    },
    bans: async (channel: string) =>
      asArray(await api<ChannelBan[]>(`/api/channels/${encodeURIComponent(channel)}/bans`)),
    unban: (channel: string, addr: string) =>
      api(`/api/channels/${encodeURIComponent(channel)}/bans?addr=${encodeURIComponent(addr)}`, {
        method: "DELETE",
      }),
    reset: (channel: string) =>
      api(`/api/channels/${encodeURIComponent(channel)}/reset`, { method: "POST" }),
    remove: (channel: string) =>
      api(`/api/channels/${encodeURIComponent(channel)}`, { method: "DELETE" }),
    logs: async (params?: { channel?: string; limit?: number }) => {
      const qs = new URLSearchParams();
      if (params?.channel) qs.set("channel", params.channel);
      qs.set("limit", String(params?.limit ?? 150));
      const data = await api<{ items: ChannelLog[] }>(`/api/channels/logs?${qs.toString()}`);
      return data?.items ?? [];
    },
    clearLogs: () => api("/api/channels/logs/clear", { method: "POST" }),
    allowlist: async () => asArray(await api<ChannelAllow[]>("/api/channels/allowlist")),
    allow: (body: { channel?: string; addr: string; reason?: string }) =>
      api("/api/channels/allowlist", { method: "POST", body: JSON.stringify(body) }),
    deny: (addr: string, channel = "") =>
      api(`/api/channels/allowlist?addr=${encodeURIComponent(addr)}&channel=${encodeURIComponent(channel)}`, {
        method: "DELETE",
      }),
    rules: async () => asArray(await api<ChannelRule[]>("/api/channels/rules")),
    addRule: (body: Partial<ChannelRule>) =>
      api<ChannelRule>("/api/channels/rules", { method: "POST", body: JSON.stringify(body) }),
    deleteRule: (id: string) =>
      api(`/api/channels/rules?id=${encodeURIComponent(id)}`, { method: "DELETE" }),
  },
  tokens: {
    list: async () => asArray(await api<{ id: number; name: string; prefix: string; scopes: string; token?: string }[]>("/api/tokens")),
    create: (name: string, scopes?: string) =>
      api<{ id: number; name: string; token?: string }>("/api/tokens", {
        method: "POST",
        body: JSON.stringify({ name, scopes: scopes || "proxies:read" }),
      }),
    remove: (id: number) => api(`/api/tokens/${id}`, { method: "DELETE" }),
  },
  audit: {
    list: (page = 1, size = 50) =>
      api<{ items: unknown[]; total: number }>(`/api/audit?page=${page}&size=${size}`),
  },
  stats: {
    traffic: () => api<import("./types").TrafficSnapshot>("/api/stats/traffic"),
    trafficHistory: (hours = 24) =>
      api<{ ts: string; up_bytes: number; down_bytes: number; active_in: number; active_out: number }[]>(
        `/api/stats/traffic/history?hours=${hours}`,
      ),
    connections: async () =>
      asArray(await api<{ id: number; channel: string; client_ip: string; upstream?: string; started_at: string }[]>("/api/stats/connections")),
  },
  healthBoard: () => api("/api/health-board"),
  scraperStats: async () => asArray(await api<unknown[]>("/api/scrapers/stats")),
  geo: async () => asArray(await api<{ region: string; count: number }[]>("/api/geo")),
  subscriptions: {
    list: async () => asArray(await api<Subscription[]>("/api/subscriptions")),
    get: (id: number | string) => api<Subscription>(`/api/subscriptions/${id}`),
    create: (body: Partial<Subscription>) =>
      api<Subscription>("/api/subscriptions", { method: "POST", body: JSON.stringify(body) }),
    update: (id: number | string, body: Partial<Subscription>) =>
      api<Subscription>(`/api/subscriptions/${id}`, { method: "PUT", body: JSON.stringify(body) }),
    remove: (id: number | string) => api(`/api/subscriptions/${id}`, { method: "DELETE" }),
    toggle: (id: number | string) => api(`/api/subscriptions/${id}/toggle`, { method: "POST" }),
    sync: (id: number | string) => api(`/api/subscriptions/${id}/sync`, { method: "POST" }),
    nodes: async (id: number | string) => asArray(await api<SubscriptionNode[]>(`/api/subscriptions/${id}/nodes`)),
    nodeAction: (id: number | string, nodeID: number | string, action: string) =>
      api(`/api/subscriptions/${id}/nodes/${nodeID}/${action}`, { method: "POST" }),
  },
  nodes: {
    list: async () => asArray(await api<ManualNode[]>("/api/manual-nodes")),
    create: (content: string) =>
      api("/api/manual-nodes", { method: "POST", body: JSON.stringify({ content }) }),
    update: (id: number | string, raw_payload: string) =>
      api(`/api/manual-nodes/${id}`, { method: "PUT", body: JSON.stringify({ raw_payload }) }),
    remove: (id: number | string) => api(`/api/manual-nodes/${id}`, { method: "DELETE" }),
    action: (id: number | string, kind: string) =>
      api(`/api/manual-nodes/${id}/${kind}`, { method: "POST" }),
  },
  pools: {
    list: async () => asArray(await api<ProxyPool[]>("/api/pools")),
    candidates: async () => asArray(await api<PoolMemberView[]>("/api/pools/available-candidates")),
    strategyTemplates: async () =>
      asArray(await api<import("./types").StrategyTemplate[]>("/api/pools/strategy-templates")),
    create: (body: Partial<ProxyPool>) =>
      api<ProxyPool>("/api/pools", { method: "POST", body: JSON.stringify(body) }),
    update: (id: number | string, body: Partial<ProxyPool>) =>
      api<ProxyPool>(`/api/pools/${id}`, { method: "PUT", body: JSON.stringify(body) }),
    remove: (id: number | string) => api(`/api/pools/${id}`, { method: "DELETE" }),
    toggle: (id: number | string) => api(`/api/pools/${id}/toggle`, { method: "POST" }),
    publish: (id: number | string) => api(`/api/pools/${id}/publish`, { method: "POST" }),
    members: (id: number | string) =>
      api<{ members?: PoolMember[]; candidates?: PoolMemberView[] }>(`/api/pools/${id}/members`),
    updateMembers: (id: number | string, members: Partial<PoolMember>[]) =>
      api(`/api/pools/${id}/members`, { method: "PUT", body: JSON.stringify({ members }) }),
  },
  settings: {
    get: () => api<Settings>("/api/settings"),
    update: (body: Settings) =>
      api<{ settings: Settings; apply_message?: string }>("/api/settings", {
        method: "PUT",
        body: JSON.stringify(body),
      }),
  },
  mihomo: {
    status: () => api<MihomoStatus>("/api/mihomo/status"),
    release: () => api("/api/mihomo/release"),
    install: (asset_name?: string) =>
      api("/api/mihomo/install", { method: "POST", body: JSON.stringify({ asset_name: asset_name || "" }) }),
  },
  system: {
    restart: () => api("/api/system/restart", { method: "POST" }),
    version: () => api<{ commit: string; short: string; time: string }>("/api/system/version"),
    updateCheck: () =>
      api<{
        local_commit: string;
        local_short: string;
        local_time: string;
        remote_commit: string;
        remote_short: string;
        newer: boolean;
        goos: string;
        goarch: string;
      }>("/api/system/update"),
    updateApply: () =>
      api("/api/system/update", { method: "POST" }),
  },
  aiProxy: {
    submit: (input: string, source: string) =>
      api<{
        submitted?: number;
        parsed?: number;
        added?: number;
        duplicates?: number;
        input_duplicates?: number;
        rejected?: number;
        net_growth?: number;
        evicted?: number;
        raw_at_cap?: boolean;
        source?: string;
        note?: string;
        [k: string]: unknown;
      }>(`/api/ai-proxy?source=${encodeURIComponent(source || "ai-unknown")}`, {
        method: "POST",
        headers: { "Content-Type": "text/plain; charset=utf-8" },
        body: input,
      }),
  },
  aiSearch: {
    run: (payload: {
      url: string;
      apikey: string;
      model?: string;
      effort?: string;
      level?: number | string;
      prompt_key?: string;
      prompt?: string;
      content?: string;
    }) =>
      api<{
        raw?: string;
        proxies?: { host: string; port: number; protocol?: string }[];
        count?: number;
        [k: string]: unknown;
      }>("/api/ai-search", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) }),
    prompts: () => api<unknown[]>("/api/ai-prompts"),
    updatePrompt: (p: { name: string; title: string; description?: string; system: string; user?: string }) =>
      api("/api/ai-prompts", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(p) }),
    deletePrompt: (name: string) => api(`/api/ai-prompts?name=${encodeURIComponent(name)}`, { method: "DELETE" }),
  },
};
