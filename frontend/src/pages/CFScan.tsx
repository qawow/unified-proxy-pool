import { useCallback, useEffect, useState } from "react";
import { endpoints } from "@/api";
import { PageHeader } from "@/components/PageHeader";
import { StatCard } from "@/components/StatCard";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Select, Textarea } from "@/components/ui/input";
import { useToast } from "@/hooks/useToast";
import type { ManualNode } from "@/types";

type Status = {
  running?: boolean;
  phase?: string;
  total?: number;
  tcp_done?: number;
  tcp_open?: number;
  tls_done?: number;
  hits?: number;
  error?: string;
};

type Hit = {
  ip: string;
  colo: string;
  fl: string;
  sni: string;
  latency_ms: number;
  last_seen?: string;
};

export function CFScanPage() {
  const { toast } = useToast();
  const [targets, setTargets] = useState("# 每行一个 IPv4 或 CIDR，最多 20000 个地址\n# 1.1.1.1\n# 172.67.0.0/24\n");
  const [st, setSt] = useState<Status>({});
  const [hits, setHits] = useState<Hit[]>([]);
  const [nodes, setNodes] = useState<ManualNode[]>([]);
  const [nodeId, setNodeId] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const [status, list, ns] = await Promise.all([
        endpoints.cfscan.status(),
        endpoints.cfscan.hits(),
        endpoints.nodes.list().catch(() => [] as ManualNode[]),
      ]);
      setSt(status || {});
      setHits(Array.isArray(list) ? list : []);
      setNodes(ns || []);
    } catch (e) {
      toast(e instanceof Error ? e.message : "加载失败", "error");
    }
  }, [toast]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!st.running) return;
    const t = window.setInterval(() => void load(), 1000);
    return () => window.clearInterval(t);
  }, [st.running, load]);

  const run = async () => {
    setBusy(true);
    try {
      await endpoints.cfscan.run({ targets });
      toast("扫描已开始", "success");
      void load();
    } catch (e) {
      toast(e instanceof Error ? e.message : "启动失败", "error");
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    const id = Number(nodeId);
    if (!id) {
      toast("先选一个已有 TLS 节点当模板（vless/trojan/vmess）", "error");
      return;
    }
    setBusy(true);
    try {
      const r = await endpoints.cfscan.apply({ node_id: id });
      toast(`已生成 ${r.created} 条手动节点`, "success");
    } catch (e) {
      toast(e instanceof Error ? e.message : "套用失败", "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <PageHeader
        title="CF 优选扫描"
        description="对照 Futureppo/Free-Fly：扫 443，再用 Cloudflare SNI 探 /cdn-cgi/trace。命中的是 CF 反代/优选 IP，不是 HTTP 代理，不会进 7892。"
        actions={
          <div className="flex gap-2">
            <Button variant="secondary" onClick={() => window.open("/api/cfscan/export", "_blank")}>
              导出 IP
            </Button>
            <Button
              variant="secondary"
              onClick={async () => {
                await endpoints.cfscan.clear();
                toast("已清空命中", "success");
                void load();
              }}
            >
              清空命中
            </Button>
          </div>
        }
      />
      <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <StatCard title="目标" value={st.total ?? 0} />
        <StatCard title="TCP 进度" value={`${st.tcp_done ?? 0}/${st.total ?? 0}`} hint={`开放 443：${st.tcp_open ?? 0}`} />
        <StatCard title="TLS 进度" value={`${st.tls_done ?? 0}/${st.tcp_open ?? 0}`} />
        <StatCard title="CF 命中" value={hits.length} hint={st.running ? `本轮 ${st.hits ?? 0}` : "库内总数"} tone="mint" />
        <StatCard title="状态" value={st.running ? st.phase || "running" : st.phase || "idle"} />
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>扫描目标</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <Textarea rows={12} value={targets} onChange={(e) => setTargets(e.target.value)} />
            <p className="text-xs text-muted-foreground">
              原理：对开放 443 的 IP 用 speed.cloudflare.com SNI 握手并 GET /cdn-cgi/trace，出现 colo=/fl= 即视为可当 CF 优选 IP。请自备 IP/CIDR，不要扫整个公网。
            </p>
            <div className="flex flex-wrap gap-2">
              <Button onClick={() => void run()} disabled={busy || st.running}>
                {st.running ? "扫描中…" : "开始扫描"}
              </Button>
              <Button variant="secondary" onClick={() => void endpoints.cfscan.stop().then(() => load())} disabled={!st.running}>
                停止
              </Button>
              <Button
                type="button"
                variant="secondary"
                disabled={busy || st.running}
                onClick={async () => {
                  try {
                    const p = await endpoints.cfscan.preset();
                    setTargets(p.targets);
                    toast("已填入 Cloudflare 官方 /24 抽样", "success");
                  } catch (e) {
                    toast(e instanceof Error ? e.message : "加载失败", "error");
                  }
                }}
              >
                填入官方 /24 抽样
              </Button>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>命中结果</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="flex flex-wrap items-end gap-2">
              <Field label="套到手动节点模板">
                <Select value={nodeId} onChange={(e) => setNodeId(e.target.value)}>
                  <option value="">选择 vless/trojan/vmess…</option>
                  {nodes.map((n) => (
                    <option key={n.id} value={n.id}>
                      {n.display_name} ({n.protocol} {n.server})
                    </option>
                  ))}
                </Select>
              </Field>
              <Button onClick={() => void apply()} disabled={busy || hits.length === 0}>
                按优选 IP 克隆节点
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              会复制模板节点，只改 server 为命中 IP，方便填进出口池。没有模板就只导出列表。
            </p>
            <div className="max-h-[420px] overflow-auto rounded-2xl border border-white/50 text-sm dark:border-white/10">
              <table className="min-w-full text-left">
                <thead className="text-xs text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2">IP</th>
                    <th className="px-3 py-2">colo</th>
                    <th className="px-3 py-2">延迟</th>
                    <th className="px-3 py-2">SNI</th>
                  </tr>
                </thead>
                <tbody>
                  {hits.length === 0 ? (
                    <tr>
                      <td className="px-3 py-6 text-muted-foreground" colSpan={4}>
                        还没有命中。扫完会按延迟排序。
                      </td>
                    </tr>
                  ) : (
                    hits.map((h) => (
                      <tr key={h.ip} className="border-t border-white/40 dark:border-white/10">
                        <td className="px-3 py-2 font-mono">{h.ip}</td>
                        <td className="px-3 py-2">{h.colo || "-"}</td>
                        <td className="px-3 py-2 tabular-nums">{h.latency_ms}ms</td>
                        <td className="px-3 py-2 text-xs">{h.sni}</td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
