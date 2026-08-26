import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { endpoints } from "@/api";
import { PageHeader } from "@/components/PageHeader";
import { StatusBadge } from "@/components/StatusBadge";
import { useSse } from "@/components/SseProvider";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input, Textarea } from "@/components/ui/input";
import { useToast } from "@/hooks/useToast";
import { formatLatency, formatTime } from "@/lib/utils";
import type { Subscription } from "@/types";

const emptyForm = {
  id: 0,
  name: "",
  url: "",
  headers_json: "",
  fetch_proxy: "",
  sync_interval_sec: 3600,
  enabled: true,
};

export function SubscriptionsPage() {
  const { toast } = useToast();
  const [items, setItems] = useState<Subscription[]>([]);
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState("");
  const [form, setForm] = useState(emptyForm);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await endpoints.subscriptions.list();
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
    const q = search.trim().toLowerCase();
    if (!q) return items;
    return items.filter((item) => item.name.toLowerCase().includes(q) || item.url.toLowerCase().includes(q));
  }, [items, search]);

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setSaving(true);
    try {
      const payload = {
        name: form.name,
        url: form.url,
        headers_json: form.headers_json,
        fetch_proxy: form.fetch_proxy,
        sync_interval_sec: Number(form.sync_interval_sec) || 0,
        enabled: form.enabled,
      };
      if (form.id) {
        await endpoints.subscriptions.update(form.id, payload);
      } else {
        await endpoints.subscriptions.create(payload);
      }
      setForm(emptyForm);
      toast("订阅已保存", "success");
      await load();
    } catch (error) {
      toast(error instanceof Error ? error.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  };

  const onAction = async (id: number, action: "sync" | "toggle" | "delete") => {
    try {
      if (action === "delete") {
        if (!confirm("确认删除该订阅？")) return;
        await endpoints.subscriptions.remove(id);
        toast("已删除", "success");
      } else if (action === "toggle") {
        await endpoints.subscriptions.toggle(id);
      } else {
        const out = (await endpoints.subscriptions.sync(id)) as {
          created_count?: number;
          updated_count?: number;
          deleted_count?: number;
          failed_count?: number;
        } | null;
        const created = out?.created_count ?? 0;
        const updated = out?.updated_count ?? 0;
        const failed = out?.failed_count ?? 0;
        toast(`同步完成：新增 ${created}，更新 ${updated}，解析失败 ${failed}`, "success");
      }
      await load();
    } catch (error) {
      toast(error instanceof Error ? error.message : "操作失败", "error");
    }
  };

  return (
    <div>
      <PageHeader title="订阅管理" description="导入并管理订阅源" />
      <div className="grid gap-4 xl:grid-cols-[360px_1fr]">
        <Card>
          <CardHeader>
            <CardTitle>{form.id ? "编辑订阅" : "新增订阅"}</CardTitle>
          </CardHeader>
          <CardContent>
            <form className="space-y-3" onSubmit={onSubmit}>
              <Field label="订阅名称">
                <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />
              </Field>
              <Field label="订阅 URL">
                <Input value={form.url} onChange={(e) => setForm({ ...form, url: e.target.value })} required />
              </Field>
              <Field label="获取时套代理">
                <Input
                  value={form.fetch_proxy}
                  onChange={(e) => setForm({ ...form, fetch_proxy: e.target.value })}
                  placeholder="空=直连；direct=单跳7892；chain=链式7893；或 socks5://user:pass@host:port"
                />
              </Field>
              <Field label="自定义请求头 JSON">
                <Textarea
                  rows={4}
                  value={form.headers_json}
                  onChange={(e) => setForm({ ...form, headers_json: e.target.value })}
                  placeholder='留空则用浏览器默认头。覆盖示例：{"User-Agent":"clash-meta/1.19","Accept":"text/plain"}'
                />
              </Field>
              <Field label="自动同步间隔（秒）">
                <Input
                  type="number"
                  min={0}
                  value={form.sync_interval_sec}
                  onChange={(e) => setForm({ ...form, sync_interval_sec: Number(e.target.value) })}
                />
              </Field>
              <label className="flex items-center gap-2 text-sm">
                <input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />
                启用订阅
              </label>
              <div className="flex gap-2">
                <Button type="submit" disabled={saving}>
                  {saving ? "保存中..." : "保存"}
                </Button>
                <Button type="button" variant="secondary" onClick={() => setForm(emptyForm)}>
                  清空
                </Button>
              </div>
            </form>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>订阅列表</CardTitle>
            <Input className="max-w-xs" placeholder="搜索订阅" value={search} onChange={(e) => setSearch(e.target.value)} />
          </CardHeader>
          <CardContent className="space-y-3">
            {loading ? <div className="text-sm text-muted-foreground">加载中...</div> : null}
            {!loading && filtered.length === 0 ? <div className="text-sm text-muted-foreground">暂无订阅</div> : null}
            {filtered.map((item) => (
              <div key={item.id} className="rounded-md border border-border p-3">
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div>
                    <Link className="font-medium text-primary hover:underline" to={`/subscriptions/${item.id}`}>
                      {item.name}
                    </Link>
                    <div className="mt-1 break-all text-xs text-muted-foreground">{item.url}</div>
                  </div>
                  <StatusBadge status={item.enabled ? item.last_sync_status || "enabled" : "disabled"} />
                </div>
                <div className="mt-2 grid gap-1 text-xs text-muted-foreground sm:grid-cols-2">
                  <div>节点：{item.total_nodes ?? 0} / 可用 {item.available_nodes ?? 0}</div>
                  <div>平均延迟：{formatLatency(item.average_latency_ms)}</div>
                  <div>最近同步：{formatTime(item.last_sync_at)}</div>
                  <div>间隔：{item.sync_interval_sec}s</div>
                </div>
                {item.last_error ? <div className="mt-2 text-xs text-danger">{item.last_error}</div> : null}
                <div className="mt-3 flex flex-wrap gap-2">
                  <Button size="sm" variant="secondary" onClick={() => setForm({
                    id: item.id,
                    name: item.name,
                    url: item.url,
                    headers_json: item.headers_json || "",
                    fetch_proxy: item.fetch_proxy || "",
                    sync_interval_sec: item.sync_interval_sec,
                    enabled: item.enabled,
                  })}>
                    编辑
                  </Button>
                  <Button size="sm" variant="secondary" onClick={() => onAction(item.id, "sync")}>同步</Button>
                  <Button size="sm" variant="secondary" onClick={() => onAction(item.id, "toggle")}>
                    {item.enabled ? "禁用" : "启用"}
                  </Button>
                  <Button size="sm" variant="danger" onClick={() => onAction(item.id, "delete")}>删除</Button>
                </div>
              </div>
            ))}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
