import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { endpoints } from "@/api";
import { PageHeader } from "@/components/PageHeader";
import { StatusBadge } from "@/components/StatusBadge";
import { useSse } from "@/components/SseProvider";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Select } from "@/components/ui/input";
import { useToast } from "@/hooks/useToast";
import { formatLatency, formatSpeed, formatTime } from "@/lib/utils";
import type { Subscription, SubscriptionNode } from "@/types";

export function SubscriptionDetailPage() {
  const { id } = useParams();
  const { toast } = useToast();
  const [sub, setSub] = useState<Subscription | null>(null);
  const [nodes, setNodes] = useState<SubscriptionNode[]>([]);
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const [protocol, setProtocol] = useState("");
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [pendingNodes, setPendingNodes] = useState<Set<string>>(new Set());
  // Guards the async load against stale responses: switching between
  // subscription ids (or an SSE-triggered load racing a manual one) must not
  // let the slower earlier response overwrite the newer data.
  const loadSeq = useRef(0);

  const load = useCallback(async () => {
    if (!id) return;
    const seq = ++loadSeq.current;
    try {
      const [detail, list] = await Promise.all([
        endpoints.subscriptions.get(id!),
        endpoints.subscriptions.nodes(id!),
      ]);
      if (seq !== loadSeq.current) return;
      setSub(detail);
      setNodes(list || []);
    } catch (error) {
      if (seq !== loadSeq.current) return;
      toast(error instanceof Error ? error.message : "加载失败", "error");
    } finally {
      if (seq === loadSeq.current) setLoading(false);
    }
  }, [id, toast]);

  useEffect(() => {
    void load();
  }, [load]);

  useSse(() => {
    void load();
  }, [load]);

  // The badge shows "disabled" for disabled nodes regardless of last_status;
  // filtering must use the same effective status or disabled nodes show up
  // under "available" and the "disabled" filter never matches anything.
  const effectiveStatus = (node: SubscriptionNode) =>
    node.enabled ? (node.last_status || "unknown").toLowerCase() : "disabled";

  const filtered = useMemo(() => {
    return nodes.filter((node) => {
      if (status && effectiveStatus(node) !== status) return false;
      if (protocol && (node.protocol || "").toLowerCase() !== protocol) return false;
      const q = search.trim().toLowerCase();
      if (!q) return true;
      return (
        node.display_name.toLowerCase().includes(q) ||
        node.server.toLowerCase().includes(q) ||
        node.protocol.toLowerCase().includes(q)
      );
    });
  }, [nodes, protocol, search, status]);

  const sync = async () => {
    setSyncing(true);
    try {
      const out = (await endpoints.subscriptions.sync(id!)) as {
        created_count?: number;
        updated_count?: number;
        deleted_count?: number;
        failed_count?: number;
        errors?: string[];
      } | null;
      const errs = out?.errors ?? [];
      const failed = out?.failed_count ?? 0;
      const summary = `同步完成：新增 ${out?.created_count ?? 0}，更新 ${out?.updated_count ?? 0}，删除 ${out?.deleted_count ?? 0}`;
      if (failed > 0 || errs.length > 0) {
        toast(`${summary}；${errs[0] || `${failed} 个节点解析失败`}`, "warning");
      } else {
        toast(summary, "success");
      }
      await load();
    } catch (error) {
      const msg = error instanceof Error ? error.message : "同步失败";
      toast(msg.includes("already running") ? "该订阅正在同步中，请稍候" : msg, "error");
    } finally {
      setSyncing(false);
    }
  };

  const nodeAction = async (nodeID: number, action: "latency-test" | "speed-test" | "toggle") => {
    const key = `${nodeID}:${action}`;
    if (pendingNodes.has(key)) return;
    setPendingNodes((s) => new Set(s).add(key));
    try {
      await endpoints.subscriptions.nodeAction(id!, nodeID, action);
      toast(action === "toggle" ? "已切换" : "已加入队列", "success");
      await load();
    } catch (error) {
      toast(error instanceof Error ? error.message : "操作失败", "error");
    } finally {
      setPendingNodes((s) => {
        const next = new Set(s);
        next.delete(key);
        return next;
      });
    }
  };

  return (
    <div>
      <PageHeader
        title={sub?.name || "订阅详情"}
        description={sub?.url || "查看节点、状态与测试"}
        actions={
          <>
            <Link to="/subscriptions" className="text-sm text-primary hover:underline">
              返回列表
            </Link>
            <Button onClick={sync} disabled={syncing}>
              {syncing ? "同步中…" : "立即同步"}
            </Button>
          </>
        }
      />

      <Card className="mb-4">
        <CardContent className="grid gap-2 pt-4 text-sm text-muted-foreground sm:grid-cols-2 lg:grid-cols-4">
          <div>状态：{sub ? <StatusBadge status={sub.enabled ? sub.last_sync_status : "disabled"} /> : "-"}</div>
          <div>最近同步：{formatTime(sub?.last_sync_at)}</div>
          <div>节点数：{nodes.length}</div>
          <div>错误：{sub?.last_error || "-"}</div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>节点列表</CardTitle>
          <div className="flex flex-wrap gap-2">
            <Input className="w-48" placeholder="搜索节点" value={search} onChange={(e) => setSearch(e.target.value)} />
            <Select value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">全部状态</option>
              <option value="available">available</option>
              <option value="unavailable">unavailable</option>
              <option value="testing">testing</option>
              <option value="disabled">disabled</option>
              <option value="unknown">unknown</option>
            </Select>
            <Select value={protocol} onChange={(e) => setProtocol(e.target.value)}>
              <option value="">全部协议</option>
              {["ss", "trojan", "vmess", "vless", "tuic", "hysteria2"].map((p) => (
                <option key={p} value={p}>{p}</option>
              ))}
            </Select>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          {loading ? <div className="text-sm text-muted-foreground">加载中...</div> : null}
          {!loading && filtered.length === 0 ? <div className="text-sm text-muted-foreground">暂无节点</div> : null}
          {filtered.map((node) => (
            <div key={node.id} className="rounded-md border border-border p-3">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div>
                  <div className="font-medium">{node.display_name}</div>
                  <div className="text-xs text-muted-foreground">
                    {node.protocol} · {node.server}:{node.port}
                  </div>
                </div>
                <StatusBadge status={effectiveStatus(node)} />
              </div>
              <div className="mt-2 grid gap-1 text-xs text-muted-foreground sm:grid-cols-3">
                <div>延迟：{formatLatency(node.last_latency_ms)}</div>
                <div>速度：{formatSpeed(node.last_speed_mbps)}</div>
                <div>测试：{formatTime(node.last_test_at)}</div>
              </div>
              {node.last_error ? <div className="mt-2 text-xs text-danger">{node.last_error}</div> : null}
              <div className="mt-3 flex flex-wrap gap-2">
                <Button size="sm" variant="secondary" disabled={pendingNodes.has(`${node.id}:latency-test`)} onClick={() => nodeAction(node.id, "latency-test")}>
                  {pendingNodes.has(`${node.id}:latency-test`) ? "入队中…" : "延迟"}
                </Button>
                <Button size="sm" variant="secondary" disabled={pendingNodes.has(`${node.id}:speed-test`)} onClick={() => nodeAction(node.id, "speed-test")}>
                  {pendingNodes.has(`${node.id}:speed-test`) ? "入队中…" : "测速"}
                </Button>
                <Button size="sm" variant="secondary" disabled={pendingNodes.has(`${node.id}:toggle`)} onClick={() => nodeAction(node.id, "toggle")}>
                  {node.enabled ? "禁用" : "启用"}
                </Button>
              </div>
            </div>
          ))}
        </CardContent>
      </Card>
    </div>
  );
}
