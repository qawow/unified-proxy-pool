import { useCallback, useEffect, useRef, useState } from "react";
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
  fail_top_sources: { name: string; fails: number }[];
};

type LogLine = {
  at: string;
  level: string;
  addr?: string;
  message: string;
  latency_ms?: number;
  source?: string;
};

export function ValidatorPage() {
  const { toast } = useToast();
  const [data, setData] = useState<Queues | null>(null);
  const [logs, setLogs] = useState<LogLine[]>([]);
  const [running, setRunning] = useState(false);
  const [loading, setLoading] = useState(true);
  const logRef = useRef<HTMLDivElement>(null);

  const load = useCallback(async () => {
    try {
      const [item, logData] = await Promise.all([
        endpoints.validator.queues(),
        endpoints.validator.logs(150).catch(() => ({ items: [], running: false })),
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

  if (loading && !data) {
    return <div className="text-sm text-muted-foreground">加载校验统计...</div>;
  }

  const levelClass = (level: string) => {
    if (level === "ok") return "text-emerald-600 dark:text-emerald-400";
    if (level === "fail") return "text-rose-600 dark:text-rose-400";
    return "text-muted-foreground";
  };

  return (
    <div>
      <PageHeader
        title="校验统计"
        description="校验队列 · 评分分布 · 简易实时日志"
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
      <div className="mb-4 grid gap-3 sm:grid-cols-3">
        <StatCard title="待校验" value={data?.raw_count ?? "-"} />
        <StatCard title="已验证" value={data?.validated_count ?? "-"} tone="success" />
        <StatCard title="失败 Top 源" value={data?.fail_top_sources?.length ?? 0} />
      </div>
      <div className="mb-4 grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>评分分布</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            {Object.keys(buckets).length === 0 ? (
              <div className="text-sm text-muted-foreground">暂无数据</div>
            ) : (
              Object.entries(buckets).map(([k, v]) => (
                <div key={k} className="flex items-center justify-between rounded border border-border px-3 py-2 text-sm">
                  <span>{k}</span>
                  <span className="font-medium">{v}</span>
                </div>
              ))
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>协议分布 / 失败源</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              {Object.entries(protocols).map(([k, v]) => (
                <div key={k} className="flex items-center justify-between rounded border border-border px-3 py-2 text-sm">
                  <span>{k}</span>
                  <span className="font-medium">{v}</span>
                </div>
              ))}
              {Object.keys(protocols).length === 0 ? <div className="text-sm text-muted-foreground">暂无协议数据</div> : null}
            </div>
            <div className="space-y-2">
              {(data?.fail_top_sources || []).map((item) => (
                <div key={item.name} className="flex items-center justify-between rounded border border-border px-3 py-2 text-sm">
                  <span>{item.name}</span>
                  <span className="text-danger">{item.fails}</span>
                </div>
              ))}
            </div>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>校验日志</CardTitle>
        </CardHeader>
        <CardContent>
          <div
            ref={logRef}
            className="max-h-80 overflow-y-auto rounded-2xl bg-black/90 p-3 font-mono text-[11px] leading-5 text-emerald-100/90"
          >
            {logs.length === 0 ? (
              <div className="text-white/40">暂无日志。点击「立即校验一批」或等待调度。</div>
            ) : (
              logs.map((line, i) => (
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
