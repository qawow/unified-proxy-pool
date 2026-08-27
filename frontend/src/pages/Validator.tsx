import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { endpoints } from "@/api";
import { PageHeader } from "@/components/PageHeader";
import { StatCard } from "@/components/StatCard";
import { useSse } from "@/components/SseProvider";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useToast } from "@/hooks/useToast";

type Queues = {
  raw_count: number;
  validated_count: number;
  score_buckets: Record<string, number>;
  protocol_counts: Record<string, number>;
  family_counts?: Record<string, number>;
  latency_buckets?: Record<string, number>;
  region_counts?: { region: string; count: number }[];
  avg_latency_ms?: number;
  fail_top_sources: { name: string; fails: number }[];
  source_stats?: {
    name: string;
    ok: number;
    fail: number;
    success_rate: number;
    avg_latency_ms: number;
    recent_ok?: number;
    recent_fail?: number;
    recent_rate?: number;
    auto_disabled: boolean;
    disabled_until?: string | null;
  }[];
  last_batch_ok?: number;
  last_batch_fail?: number;
  last_batch_raw?: number;
  last_batch_recheck?: number;
  last_batch_at?: string | null;
  last_batch_ms?: number;
  running?: boolean;
  batch_size?: number;
  batch_done?: number;
  lifetime_ok?: number;
  lifetime_fail?: number;
  lifetime_batches?: number;
  history?: {
    ok: number;
    fail: number;
    raw: number;
    recheck: number;
    duration_ms: number;
    at: string;
  }[];
};

type LogLine = {
  at: string;
  level: string;
  addr?: string;
  message: string;
  latency_ms?: number;
  source?: string;
};

const FAMILY_LABEL: Record<string, string> = {
  ipv4: "IPv4",
  ipv6: "IPv6",
  unknown: "未知",
};

function bar(count: number, max: number) {
  if (max <= 0) return 0;
  return Math.max(2, Math.round((count / max) * 100));
}

export function ValidatorPage() {
  const { toast } = useToast();
  const [data, setData] = useState<Queues | null>(null);
  const [logs, setLogs] = useState<LogLine[]>([]);
  const [running, setRunning] = useState(false);
  const [loading, setLoading] = useState(true);
  const [logFilter, setLogFilter] = useState<"all" | "ok" | "fail" | "skip">("all");
  const logRef = useRef<HTMLDivElement>(null);

  const load = useCallback(async () => {
    try {
      const [item, logData] = await Promise.all([
        endpoints.validator.queues(),
        endpoints.validator.logs(200).catch(() => ({ items: [], running: false })),
      ]);
      setData(item);
      setLogs(logData.items || []);
      setRunning(Boolean(logData.running));
    } catch (error) {
      toast(error instanceof Error ? error.message : "加载失败", "error");
    } finally {
      setLoading(false);
    }
  }, [toast]);

  useEffect(() => {
    void load();
    const t = window.setInterval(() => void load(), 2500);
    return () => window.clearInterval(t);
  }, [load]);

  useSse(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (logRef.current) {
      logRef.current.scrollTop = logRef.current.scrollHeight;
    }
  }, [logs]);

  const reenable = async (name: string) => {
    try {
      await endpoints.validator.reenable(name);
      toast(`已恢复 ${name}`, "success");
      void load();
    } catch (error) {
      toast(error instanceof Error ? error.message : "恢复失败", "error");
    }
  };

  const run = async () => {
    try {
      await endpoints.validator.run();
      toast("已触发校验批次", "success");
      void load();
    } catch (error) {
      toast(error instanceof Error ? error.message : "触发失败", "error");
    }
  };

  const clearLogs = async () => {
    try {
      await endpoints.validator.clearLogs();
      setLogs([]);
      toast("日志已清空", "success");
    } catch (error) {
      toast(error instanceof Error ? error.message : "清空失败", "error");
    }
  };

  const buckets = data?.score_buckets || {};
  const protocols = data?.protocol_counts || {};
  const families = data?.family_counts || {};
  const latBuckets = data?.latency_buckets || {};
  const regions = data?.region_counts || [];
  const fails = data?.fail_top_sources || [];
  const sourceStats = data?.source_stats || [];
  const raw = data?.raw_count ?? 0;
  const ok = data?.validated_count ?? 0;
  const total = raw + ok;
  const health = total > 0 ? `${((ok / total) * 100).toFixed(1)}%` : "—";
  const lastOK = data?.last_batch_ok ?? 0;
  const lastFail = data?.last_batch_fail ?? 0;
  const lastTotal = lastOK + lastFail;
  const lastRate = lastTotal > 0 ? `${((lastOK / lastTotal) * 100).toFixed(0)}%` : "—";
  const lastDur = data?.last_batch_ms ? `${(data.last_batch_ms / 1000).toFixed(1)}s` : "—";
  const avgLat = data?.avg_latency_ms ? `${Math.round(data.avg_latency_ms)}ms` : "—";
  const lifeOK = data?.lifetime_ok ?? 0;
  const lifeFail = data?.lifetime_fail ?? 0;
  const lifeN = data?.lifetime_batches ?? 0;
  const batchSize = data?.batch_size ?? 0;
  const batchDone = data?.batch_done ?? lastTotal;
  const history = data?.history || [];

  const visibleLogs = useMemo(() => {
    if (logFilter === "all") return logs;
    return logs.filter((l) => l.level === logFilter);
  }, [logs, logFilter]);

  if (loading && !data) {
    return <div className="text-sm text-muted-foreground">加载校验统计...</div>;
  }

  const levelClass = (level: string) => {
    if (level === "ok") return "text-emerald-600 dark:text-emerald-400";
    if (level === "fail") return "text-rose-600 dark:text-rose-400";
    return "text-muted-foreground";
  };

  const Dist = ({
    title,
    entries,
    label,
  }: {
    title: string;
    entries: [string, number][];
    label?: (k: string) => string;
  }) => {
    const max = entries.reduce((m, [, v]) => Math.max(m, v), 0);
    return (
      <Card>
        <CardHeader>
          <CardTitle>{title}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          {entries.length === 0 ? (
            <div className="text-sm text-muted-foreground">暂无数据</div>
          ) : (
            entries.map(([k, v]) => (
              <div key={k} className="space-y-1">
                <div className="flex items-center justify-between text-sm">
                  <span>{label ? label(k) : k}</span>
                  <span className="font-medium tabular-nums">{v}</span>
                </div>
                <div className="h-1.5 overflow-hidden rounded-full bg-white/50 dark:bg-white/10">
                  <div className="h-full rounded-full bg-sky-500/70" style={{ width: `${bar(v, max)}%` }} />
                </div>
              </div>
            ))
          )}
        </CardContent>
      </Card>
    );
  };

  return (
    <div>
      <PageHeader
        title="校验统计"
        description="累计通过/失败不会在一轮结束后清零。本批数字跑完会冻结，直到下一轮开始。"
        actions={
          <div className="flex gap-2">
            {running ? <span className="soft-pill self-center">校验中…</span> : null}
            <Button variant="secondary" onClick={() => void clearLogs()}>
              清空日志
            </Button>
            <Button onClick={run}>立即校验一批</Button>
          </div>
        }
      />
      <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard title="待校验" value={raw} hint="还没验过或需要复验" tone="amber" />
        <StatCard title="已验证" value={ok} hint={`池内健康率 ${health} · 均延迟 ${avgLat}`} tone="mint" />
        <StatCard title="累计通过" value={lifeOK} hint={`共 ${lifeN} 轮 · 失败 ${lifeFail}`} tone="mint" />
        <StatCard
          title={running ? "本批进行中" : "本批（已结束）"}
          value={`${lastOK} / ${lastFail}`}
          hint={batchSize > 0 ? `进度 ${batchDone}/${batchSize} · 成功率 ${lastRate} · ${lastDur}` : `成功率 ${lastRate} · ${lastDur}`}
        />
      </div>
      <div className="mb-4 grid gap-4 lg:grid-cols-4">
        <Dist title="评分分布" entries={Object.entries(buckets)} />
        <Dist title="协议分布" entries={Object.entries(protocols)} />
        <Dist title="延迟分布" entries={Object.entries(latBuckets)} />
        <Dist
          title="IP 家族"
          entries={Object.entries(families)}
          label={(k) => FAMILY_LABEL[k] || k}
        />
      </div>
      <div className="mb-4 grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>最近轮次（不清空）</CardTitle>
          </CardHeader>
          <CardContent>
            {history.length === 0 ? (
              <div className="text-sm text-muted-foreground">还没有完成的校验轮次。</div>
            ) : (
              <div className="space-y-2">
                {history.map((h, i) => (
                  <div key={`${h.at}-${i}`} className="flex items-center justify-between rounded-2xl border border-white/50 px-3 py-2 text-sm dark:border-white/10">
                    <span className="text-muted-foreground">{h.at ? new Date(h.at).toLocaleString() : `#${i + 1}`}</span>
                    <span className="tabular-nums">
                      <span className="text-emerald-600 dark:text-emerald-400">{h.ok}</span>
                      {" / "}
                      <span className="text-rose-600 dark:text-rose-400">{h.fail}</span>
                      {h.duration_ms ? <span className="text-muted-foreground"> · {(h.duration_ms / 1000).toFixed(1)}s</span> : null}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
        <Dist
          title="地区 Top"
          entries={regions.map((r) => [r.region || "??", r.count])}
        />
        <Card>
          <CardHeader>
            <CardTitle>来源成功率</CardTitle>
          </CardHeader>
          <CardContent>
            {sourceStats.length === 0 && fails.length === 0 ? (
              <div className="text-sm text-muted-foreground">跑过一轮校验后，这里按来源统计通过/失败。</div>
            ) : (
              <div className="space-y-2">
                {(sourceStats.length ? sourceStats : fails.map((f) => ({ name: f.name, ok: 0, fail: f.fails, success_rate: 0, avg_latency_ms: 0, recent_ok: 0, recent_fail: f.fails, recent_rate: 0, auto_disabled: false }))).map((item) => (
                  <div key={item.name} className="flex items-center justify-between gap-2 rounded-2xl border border-white/50 px-3 py-2 text-sm dark:border-white/10">
                    <span className="font-mono text-xs">
                      {item.name}
                      {item.auto_disabled ? <span className="ml-2 text-rose-500">已自动停用</span> : null}
                    </span>
                    <span className="flex items-center gap-2 tabular-nums text-muted-foreground">
                      近 {item.recent_ok ?? 0}/{item.recent_fail ?? 0}
                      {typeof item.recent_rate === "number" ? ` · ${(item.recent_rate * 100).toFixed(0)}%` : ""}
                      <span className="text-white/40">终身 {item.ok}/{item.fail}</span>
                      {item.auto_disabled ? (
                        <Button size="sm" variant="secondary" onClick={() => void reenable(item.name)}>
                          恢复
                        </Button>
                      ) : null}
                    </span>
                  </div>
                ))}
              </div>
            )}
        </CardContent>
      </Card>
      </div>

      <Card>
        <CardHeader className="flex flex-wrap items-center justify-between gap-3">
          <CardTitle>校验日志 · {visibleLogs.length} 条</CardTitle>
          <div className="flex gap-1">
            {(["all", "ok", "fail", "skip"] as const).map((k) => (
              <Button key={k} size="sm" variant={logFilter === k ? "primary" : "ghost"} onClick={() => setLogFilter(k)}>
                {k === "all" ? "全部" : k === "ok" ? "通过" : k === "skip" ? "跳过" : "失败"}
              </Button>
            ))}
          </div>
        </CardHeader>
        <CardContent>
          <div
            ref={logRef}
            className="max-h-80 overflow-y-auto rounded-2xl bg-black/90 p-3 font-mono text-[11px] leading-5 text-emerald-100/90"
          >
            {visibleLogs.length === 0 ? (
              <div className="text-white/40">暂无日志。点击「立即校验一批」或等待调度。</div>
            ) : (
              visibleLogs.map((line, i) => (
                <div key={i} className="whitespace-pre-wrap break-all">
                  <span className="text-white/40">{line.at ? new Date(line.at).toLocaleTimeString() : ""}</span>{" "}
                  <span className={levelClass(line.level)}>[{line.level}]</span>{" "}
                  {line.addr ? <span className="text-sky-300">{line.addr} </span> : null}
                  <span>{line.message}</span>
                  {line.latency_ms ? <span className="text-white/50"> · {line.latency_ms}ms</span> : null}
                  {line.source ? <span className="text-white/40"> · {line.source}</span> : null}
                </div>
              ))
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
