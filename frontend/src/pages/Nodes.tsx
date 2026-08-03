import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { endpoints } from "@/api";
import { PageHeader } from "@/components/PageHeader";
import { StatusBadge } from "@/components/StatusBadge";
import { useSse } from "@/components/SseProvider";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { useToast } from "@/hooks/useToast";
import { formatLatency, formatSpeed, formatTime } from "@/lib/utils";
import type { ManualNode } from "@/types";

export function NodesPage() {
  const { toast } = useToast();
  const [items, setItems] = useState<ManualNode[]>([]);
  const [content, setContent] = useState("");
  const [editId, setEditId] = useState(0);
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const [protocol, setProtocol] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await endpoints.nodes.list();
      setItems(data || []);
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

  const filtered = useMemo(() => {
    return items.filter((node) => {
      if (status && (node.last_status || "").toLowerCase() !== status) return false;
      if (protocol && (node.protocol || "").toLowerCase() !== protocol) return false;
      const q = search.trim().toLowerCase();
      if (!q) return true;
      return (
        node.display_name.toLowerCase().includes(q) ||
        node.server.toLowerCase().includes(q) ||
        node.protocol.toLowerCase().includes(q)
      );
    });
  }, [items, protocol, search, status]);

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setSaving(true);
    try {
      if (editId) {
        await endpoints.nodes.update(editId, content);
      } else {
        await endpoints.nodes.create(content);
      }
      setContent("");
      setEditId(0);
      toast("节点已保存", "success");
      await load();
    } catch (error) {
      toast(error instanceof Error ? error.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  };

  const action = async (id: number, kind: "latency-test" | "speed-test" | "toggle" | "delete") => {
    try {
      if (kind === "delete") {
        if (!confirm("确认删除该节点？")) return;
        await endpoints.nodes.remove(id);
      } else {
        await endpoints.nodes.action(id, kind);
      }
      await load();
    } catch (error) {
      toast(error instanceof Error ? error.message : "操作失败", "error");
    }
  };

  return (
    <div>
      <PageHeader title="手动节点" description="添加原始节点并管理状态" />
      <div className="grid gap-4 xl:grid-cols-[360px_1fr]">
        <Card>
          <CardHeader>
            <CardTitle>{editId ? "编辑节点" : "新增节点"}</CardTitle>
          </CardHeader>
          <CardContent>
            <form className="space-y-3" onSubmit={onSubmit}>
              <Field label="节点内容">
                <Textarea
                  rows={12}
                  required
                  value={content}
                  onChange={(e) => setContent(e.target.value)}
                  placeholder="支持单条 URI、多条 URI、Mihomo/Clash YAML 片段"
                />
              </Field>
              <div className="flex gap-2">
                <Button type="submit" disabled={saving}>{saving ? "保存中..." : "保存"}</Button>
                <Button type="button" variant="secondary" onClick={() => { setContent(""); setEditId(0); }}>清空</Button>
              </div>
            </form>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>节点列表</CardTitle>
            <div className="flex flex-wrap gap-2">
              <Input className="w-44" placeholder="搜索" value={search} onChange={(e) => setSearch(e.target.value)} />
              <Select value={status} onChange={(e) => setStatus(e.target.value)}>
                <option value="">全部状态</option>
                <option value="available">available</option>
                <option value="unavailable">unavailable</option>
                <option value="testing">testing</option>
                <option value="disabled">disabled</option>
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
                  <StatusBadge status={node.enabled ? node.last_status : "disabled"} />
                </div>
                <div className="mt-2 grid gap-1 text-xs text-muted-foreground sm:grid-cols-3">
                  <div>延迟：{formatLatency(node.last_latency_ms)}</div>
                  <div>速度：{formatSpeed(node.last_speed_mbps)}</div>
                  <div>测试：{formatTime(node.last_test_at)}</div>
                </div>
                <div className="mt-3 flex flex-wrap gap-2">
                  <Button size="sm" variant="secondary" onClick={() => { setEditId(node.id); setContent(node.raw_payload || ""); }}>编辑</Button>
                  <Button size="sm" variant="secondary" onClick={() => action(node.id, "latency-test")}>延迟</Button>
                  <Button size="sm" variant="secondary" onClick={() => action(node.id, "speed-test")}>测速</Button>
                  <Button size="sm" variant="secondary" onClick={() => action(node.id, "toggle")}>{node.enabled ? "禁用" : "启用"}</Button>
                  <Button size="sm" variant="danger" onClick={() => action(node.id, "delete")}>删除</Button>
                </div>
              </div>
            ))}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
