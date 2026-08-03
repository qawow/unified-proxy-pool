import { useCallback, useEffect, useMemo, useState } from "react";
import { Activity, Gauge, Globe2, Radar, Server, ShieldCheck } from "lucide-react";
import { endpoints } from "@/api";
import { PageHeader } from "@/components/PageHeader";
import { StatCard } from "@/components/StatCard";
import { useSse } from "@/components/SseProvider";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useToast } from "@/hooks/useToast";
import type { DirectProxyStatus, Overview, Settings } from "@/types";

function formatNum(n?: number | null) {
  if (n === null || n === undefined || Number.isNaN(n)) return "-";
  return n.toLocaleString("en-US");
}

function formatBytes(n?: number | null) {
  if (n === null || n === undefined || Number.isNaN(n)) return "-";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 2)} ${u[i]}`;
}

function chainNodes(hops: number): string[] {
  const n = Math.max(2, Math.min(4, hops || 2));
  const nodes = ["本机", "入口"];
  for (let i = 0; i < n - 2; i++) nodes.push("中继");
  nodes.push("出口", "目标");
  return nodes;
}

export function DashboardPage() {
  const { toast } = useToast();
  const [data, setData] = useState<Overview | null>(null);
  const [direct, setDirect] = useState<DirectProxyStatus | null>(null);
  const [cards, setCards] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(true);

  const vis = (key: string) => cards[key] !== false;

  const load = useCallback(async () => {
    try {
      const [item, dp, st] = await Promise.all([
        endpoints.overview(),
        endpoints.directProxy.status().catch(() => null),
        endpoints.settings.get().catch(() => null as unknown as Settings),
      ]);
      setData(item);
      setDirect(dp);
      const c = st?.feature?.dashboard_cards;
      if (c && typeof c === "object") setCards(c as Record<string, boolean>);
    } catch (error) {
      toast(error instanceof Error ? error.message : "加载失败", "error");
    } finally {
      setLoading(false);
    }
  }, [toast]);

  useEffect(() => {
    void load();
  }, [load]);

  useSse(() => {
    void load();
  }, [load]);

  const healthRate = useMemo(() => {
    const total = data?.total_proxies || 0;
    const ok = data?.validated_proxies || 0;
    if (!total) return "0%";
    return `${((ok / total) * 100).toFixed(1)}%`;
  }, [data]);

  if (loading && !data) {
    return <div className="text-sm text-muted-foreground">加载仪表盘...</div>;
  }

  const copy = (text: string) => {
    if (text) void navigator.clipboard?.writeText(text);
    toast("已复制", "success");
  };

  const activeIn = data?.traffic?.active_in ?? 0;
  const activeOut = data?.traffic?.active_out ?? 0;
  const hops = direct?.chain_hops || 2;
  const pathNodes = chainNodes(hops);
  const pathLabel = direct?.chain_path || pathNodes.join(" → ");
  const chainCh = data?.traffic?.by_channel?.chain;

  return (
    <div className="anim-fade-up">
      <PageHeader
        title="仪表盘"
        description={`后端 ${data?.backend || "-"} · Redis ${data?.redis_ok ? "已连接" : "内存模式"} · DirectProxy ${direct?.running ? "运行中" : "未运行"}`}
      />

      {(vis("available") || vis("health")) && (
      <div className="mb-5 grid gap-4 md:grid-cols-2 anim-stagger">
        {vis("available") ? (
        <StatCard
          title="实时可用代理"
          value={formatNum(data?.validated_proxies)}
          hint={`原始待验 ${formatNum(data?.raw_proxies)} · 总计 ${formatNum(data?.total_proxies)}`}
          icon={Gauge}
          tone="amber"
        />
        ) : null}
        {vis("health") ? (
        <StatCard
          title="节点健康率"
          value={healthRate}
          hint={`健康 ${formatNum(data?.validated_proxies)} / 总计 ${formatNum(data?.total_proxies)}`}
          icon={ShieldCheck}
          tone="mint"
          badge={`${formatNum(data?.total_proxies)} IP`}
        />
        ) : null}
      </div>
      )}

      <div className="mb-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-4 anim-stagger">
        {vis("live_conn") ? (
        <StatCard
          title="实时连接数"
          value={formatNum(data?.traffic?.active_conns)}
          hint={`入站 ${formatNum(activeIn)} · 出站 ${formatNum(activeOut)}`}
          icon={Activity}
          tone="amber"
          badge="LIVE"
        />
        ) : null}
        {vis("up_bytes") ? <StatCard title="上行流量" value={formatBytes(data?.traffic?.up_bytes)} hint={`累计连接 ${formatNum(data?.traffic?.connections)}`} icon={Activity} tone="sky" /> : null}
        {vis("down_bytes") ? <StatCard title="下行流量" value={formatBytes(data?.traffic?.down_bytes)} hint={`成功 ${formatNum(data?.traffic?.success)} / 失败 ${formatNum(data?.traffic?.fail)}`} icon={Server} tone="mint" /> : null}
        {vis("sources") ? <StatCard title="采集源" value={`${data?.enabled_sources ?? "-"} / ${data?.source_count ?? "-"}`} hint="已启用 / 全部" icon={Radar} tone="sky" /> : null}
        {vis("avg_score") ? <StatCard title="平均评分" value={data ? data.avg_score.toFixed(1) : "-"} hint="已验证代理" icon={Activity} tone="violet" /> : null}
        {vis("total") ? <StatCard title="代理总数" value={formatNum(data?.total_proxies)} hint="含未验证" icon={Server} tone="sky" /> : null}
        {vis("single_hop") ? <StatCard title="单跳出口" value={direct?.running ? "在线" : "离线"} hint={String(direct?.client_http || "-")} icon={Globe2} tone="mint" /> : null}
      </div>

      {vis("chain") && direct?.chain_enabled ? (
        <div className="mb-5 grid gap-4 lg:grid-cols-2">
          <div className="glass-card relative overflow-hidden p-5">
            <div className="absolute right-4 top-4">
              <span className="soft-pill">{direct?.chain_running ? "CHAIN" : "OFF"}</span>
            </div>
            <div className="flex items-start gap-3">
              <div className="icon-blob icon-blob-violet">
                <ShieldCheck className="h-5 w-5" strokeWidth={2.2} />
              </div>
              <div className="min-w-0 flex-1 pr-12">
                <div className="text-[13px] font-medium text-muted-foreground">链式代理</div>
                <div className="mt-1.5 text-[1.75rem] font-bold leading-none tracking-tight tabular-nums">
                  {direct?.chain_running ? `${hops} 跳` : "离线"}
                </div>
                <div className="mt-3 flex flex-wrap items-center gap-1.5">
                  {pathNodes.map((n, i) => (
                    <span key={`${n}-${i}`} className="inline-flex items-center gap-1.5">
                      <span
                        className={
                          n === "入口" || n === "出口"
                            ? "rounded-full bg-violet-500/15 px-2.5 py-1 text-[11px] font-semibold text-violet-700 dark:text-violet-300"
                            : n === "中继"
                              ? "rounded-full bg-amber-500/15 px-2.5 py-1 text-[11px] font-medium text-amber-700 dark:text-amber-300"
                              : "rounded-full bg-white/60 px-2.5 py-1 text-[11px] text-muted-foreground dark:bg-white/10"
                        }
                      >
                        {n}
                      </span>
                      {i < pathNodes.length - 1 ? <span className="text-xs text-muted-foreground/70">→</span> : null}
                    </span>
                  ))}
                </div>
                <div className="mt-2 text-xs text-muted-foreground">{pathLabel}</div>
                <div className="mt-1 text-xs text-muted-foreground">
                  入站 {formatNum(chainCh?.active_in)} · 出站 {formatNum(chainCh?.active_out)} · 成功{" "}
                  {direct?.chain_success ?? 0} / 失败 {direct?.chain_failures ?? 0}
                </div>
              </div>
            </div>
          </div>
          <StatCard
            title="链路出口"
            value={String(direct?.chain_http || "-").replace(/^https?:\/\//, "")}
            hint={`请求 ${direct?.chain_requests ?? 0} · 复制地址见下方局域网`}
            icon={Globe2}
            tone="sky"
          />
        </div>
      ) : null}

      {vis("lan") && (data?.panel_hint || direct?.client_http) ? (
        <Card className="mb-5">
          <CardHeader>
            <CardTitle>局域网访问</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3 pt-0 text-sm">
            {[
              { label: "面板", value: String(data?.panel_hint || "-") },
              { label: "单跳 HTTP", value: String(direct?.client_http || "-") },
              { label: "单跳 SOCKS", value: String(direct?.client_socks5 || "-") },
              ...(direct?.chain_enabled
                ? [
                    { label: "链式 HTTP", value: String(direct?.chain_http || "-") },
                    { label: "链式 SOCKS", value: String(direct?.chain_socks5 || "-") },
                  ]
                : []),
            ].map((row) => (
              <div key={row.label} className="flex flex-wrap items-center gap-2 text-muted-foreground">
                <span className="w-20 text-xs font-medium">{row.label}</span>
                <span className="rounded-full bg-white/60 px-3 py-1 font-mono text-xs text-foreground dark:bg-white/10">
                  {row.value}
                </span>
                <button
                  type="button"
                  className="rounded-full border border-white/60 bg-white/50 px-2.5 py-0.5 text-xs hover:bg-white/80 dark:border-white/10 dark:bg-white/10"
                  onClick={() => copy(row.value)}
                >
                  复制
                </button>
              </div>
            ))}
            <div className="break-all rounded-2xl bg-white/40 px-3 py-2 font-mono text-[11px] text-muted-foreground dark:bg-white/5">
              {direct?.client_examples?.curl || ""}
            </div>
          </CardContent>
        </Card>
      ) : null}

      <div className="grid gap-4 lg:grid-cols-2">
        {vis("events") ? (
        <Card>
          <CardHeader>
            <CardTitle>最近动态</CardTitle>
          </CardHeader>
          <CardContent className="pt-0">
            <div className="max-h-72 space-y-2 overflow-y-auto">
              {(data?.recent_events || []).length === 0 ? (
                <div className="rounded-2xl border border-dashed border-border/80 p-8 text-center text-sm text-muted-foreground">
                  暂无事件，采集/校验开始后将在此展示
                </div>
              ) : (
                data?.recent_events.map((ev, idx) => (
                  <div
                    key={idx}
                    className="rounded-2xl bg-white/50 px-3.5 py-2.5 text-xs text-muted-foreground dark:bg-white/5"
                  >
                    {ev}
                  </div>
                ))
              )}
            </div>
          </CardContent>
        </Card>
        ) : null}
        {vis("regions") ? (
        <Card>
          <CardHeader>
            <CardTitle>地区分布 Top</CardTitle>
          </CardHeader>
          <CardContent className="pt-0">
            <div className="space-y-2">
              {(data?.region_top || []).length === 0 ? (
                <div className="rounded-2xl border border-dashed border-border/80 p-8 text-center text-sm text-muted-foreground">
                  校验通过并带地区信息后显示
                </div>
              ) : (
                (data?.region_top || []).map((r) => (
                  <div
                    key={r.region}
                    className="flex items-center justify-between rounded-2xl bg-white/50 px-3.5 py-2.5 text-sm dark:bg-white/5"
                  >
                    <span>{r.region || "unknown"}</span>
                    <span className="tabular-nums text-muted-foreground">{r.count}</span>
                  </div>
                ))
              )}
            </div>
          </CardContent>
        </Card>
        ) : null}
      </div>
    </div>
  );
}
