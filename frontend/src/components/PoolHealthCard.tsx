import { Link } from "react-router-dom";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { PoolHealth } from "@/types";

const statusLabels: Record<PoolHealth["status"], string> = {
  healthy: "校验良好", degraded: "余量有限", slow: "延迟偏高",
  critical: "备用不足", empty: "暂无可用", unknown: "待确认",
};
const reasonLabels: Record<string, string> = {
  timeout: "超时", connect: "连接失败", tls: "TLS / 证书失败",
  fail: "其他失败", aborted: "校验中断", blocked_country: "地区过滤",
};
const ms = (value: number) => value > 0 ? `${value.toLocaleString()} ms` : "待测";

export function PoolHealthCard({ health }: { health: PoolHealth }) {
  const unavailable = health.status === "unknown" && health.available === 0;
  const reasons = Object.entries(health.last_fail_reasons ?? {}).filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1]);
  return (
    <Card className="mb-5">
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center justify-between gap-2">
          <span>免费代理出口池</span>
          <span className="soft-pill text-xs">{statusLabels[health.status] ?? "待确认"}</span>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div aria-live="polite">
          <p className="font-medium">{health.verdict}</p>
          {health.advice && <p className="mt-1 text-sm text-muted-foreground">{health.advice}</p>}
        </div>
        {!unavailable && (
          <>
            <dl className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
              {[
                ["校验通过", `${health.available.toLocaleString()} 个`],
                ["等待校验", `${health.raw_pending.toLocaleString()} 个`],
                ["延迟中位数", ms(health.median_latency_ms)],
                ["最快校验延迟", ms(health.fastest_latency_ms)],
              ].map(([label, value]) => (
                <div key={label} className="rounded-xl bg-muted/40 p-3">
                  <dt className="text-xs text-muted-foreground">{label}</dt>
                  <dd className="mt-1 font-semibold tabular-nums">{value}</dd>
                </div>
              ))}
            </dl>
            <p className="text-sm text-muted-foreground">
              快（&lt;500 ms）{health.fast} 个 · 一般（500–1500 ms）{health.usable} 个 · 慢（&gt;1500 ms）{health.slow} 个
              {(health.unknown_latency ?? 0) > 0 && ` · 延迟待测 ${health.unknown_latency} 个`}
            </p>
          </>
        )}
        {reasons.length > 0 && (
          <div className="text-sm">
            <p className="mb-2 text-muted-foreground">最近一批校验失败原因</p>
            <div className="flex flex-wrap gap-2">
              {reasons.map(([reason, count]) => <span key={reason} className="soft-pill text-xs">{reasonLabels[reason] ?? reason} {count}</span>)}
            </div>
          </div>
        )}
        <p className="text-xs text-muted-foreground">延迟来自代理校验；下载速度和目标网站的可访问性需要实际连接确认。</p>
        <nav aria-label="出口池操作" className="flex flex-wrap gap-4 text-sm font-medium">
          <Link className="text-primary hover:underline" to="/proxies">查看代理列表</Link>
          <Link className="text-primary hover:underline" to="/validator">查看校验进度与日志</Link>
          <Link className="text-primary hover:underline" to="/settings">配置代理出口</Link>
        </nav>
      </CardContent>
    </Card>
  );
}
