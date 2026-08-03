import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { endpoints } from "@/api";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { PageHeader } from "@/components/PageHeader";
import { StatusBadge } from "@/components/StatusBadge";
import { useSse } from "@/components/SseProvider";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { useToast } from "@/hooks/useToast";
import { formatTime } from "@/lib/utils";
import type { Scraper } from "@/types";

const FORMATS = [
  { value: "plaintext", label: "纯文本 (ip:port)" },
  { value: "json", label: "JSON / JSONL" },
  { value: "html_table", label: "HTML 表格" },
  { value: "html_regex", label: "HTML 正则" },
  { value: "socks_list", label: "SOCKS 列表" },
];

const emptyForm = {
  name: "",
  urls: "",
  format: "plaintext",
  protocol: "http",
  enabled: true,
  fragile: false,
  host_col: 0,
  port_col: 1,
};

export function SourcesPage() {
  const { toast } = useToast();
  const [items, setItems] = useState<Scraper[]>([]);
  const [q, setQ] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState(emptyForm);
  const [editing, setEditing] = useState<string | null>(null);
  const [pendingDelete, setPendingDelete] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await endpoints.scrapers.list();
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
    const keyword = q.trim().toLowerCase();
    if (!keyword) return items;
    return items.filter(
      (item) =>
        item.name.toLowerCase().includes(keyword) ||
        item.protocol.toLowerCase().includes(keyword) ||
        (item.url_hint || "").toLowerCase().includes(keyword) ||
        (item.format || "").toLowerCase().includes(keyword),
    );
  }, [items, q]);

  const toggle = async (name: string) => {
    setBusy(name);
    try {
      await endpoints.scrapers.toggle(name);
      await load();
    } catch (error) {
      toast(error instanceof Error ? error.message : "切换失败", "error");
    } finally {
      setBusy(null);
    }
  };

  const runOne = async (name: string) => {
    setBusy(name);
    try {
      await endpoints.scrapers.run(name);
      toast(`已启动 ${name}（后台采集）`, "success");
      setTimeout(() => void load(), 1500);
    } catch (error) {
      toast(error instanceof Error ? error.message : "运行失败", "error");
    } finally {
      setBusy(null);
    }
  };

  const runAll = async () => {
    try {
      await endpoints.scrapers.runAll();
      toast("已触发全部启用源采集", "success");
    } catch (error) {
      toast(error instanceof Error ? error.message : "触发失败", "error");
    }
  };

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const urls = form.urls
      .split("\n")
      .map((u) => u.trim())
      .filter(Boolean);
    const body = {
      name: form.name.trim(),
      urls,
      format: form.format,
      protocol: form.protocol,
      enabled: form.enabled,
      fragile: form.fragile,
      host_col: form.host_col,
      port_col: form.port_col,
    };
    try {
      if (editing) {
        await endpoints.scrapers.update(editing, body);
        toast("采集源已更新", "success");
      } else {
        await endpoints.scrapers.create(body);
        toast("采集源已添加", "success");
      }
      setForm(emptyForm);
      setEditing(null);
      setShowForm(false);
      await load();
    } catch (error) {
      toast(error instanceof Error ? error.message : "保存失败", "error");
    }
  };

  const startEdit = (item: Scraper) => {
    if (item.builtin) {
      toast("内置源不可编辑", "error");
      return;
    }
    setEditing(item.name);
    setForm({
      name: item.name,
      urls: (item.urls || []).join("\n") || item.url_hint || "",
      format: item.format || "plaintext",
      protocol: item.protocol || "http",
      enabled: item.enabled,
      fragile: item.fragile,
      host_col: 0,
      port_col: 1,
    });
    setShowForm(true);
  };

  const confirmDelete = async () => {
    if (!pendingDelete) return;
    setDeleting(true);
    try {
      await endpoints.scrapers.remove(pendingDelete);
      toast(`已删除 ${pendingDelete}`, "success");
      setPendingDelete(null);
      await load();
    } catch (error) {
      toast(error instanceof Error ? error.message : "删除失败", "error");
    } finally {
      setDeleting(false);
    }
  };

  const enabledCount = items.filter((i) => i.enabled).length;

  return (
    <div className="anim-fade-up">
      <PageHeader
        title="采集源"
        description={`共 ${items.length} 个源，已启用 ${enabledCount}（支持 Web 添加多种格式）`}
        actions={
          <div className="flex gap-2">
            <Button variant="secondary" onClick={() => { setShowForm((v) => !v); setEditing(null); setForm(emptyForm); }}>
              {showForm ? "收起表单" : "添加采集源"}
            </Button>
            <Button onClick={() => void runAll()}>运行全部启用源</Button>
          </div>
        }
      />

      {showForm ? (
        <Card className="mb-4 anim-fade-up">
          <CardHeader>
            <CardTitle>{editing ? `编辑 ${editing}` : "新增采集源"}</CardTitle>
          </CardHeader>
          <CardContent>
            <form className="space-y-3" onSubmit={onSubmit}>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="名称">
                  <Input
                    required
                    disabled={Boolean(editing)}
                    value={form.name}
                    onChange={(e) => setForm({ ...form, name: e.target.value })}
                    placeholder="my-source"
                  />
                </Field>
                <Field label="格式">
                  <Select value={form.format} onChange={(e) => setForm({ ...form, format: e.target.value })}>
                    {FORMATS.map((f) => (
                      <option key={f.value} value={f.value}>
                        {f.label}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Field label="协议">
                  <Select value={form.protocol} onChange={(e) => setForm({ ...form, protocol: e.target.value })}>
                    {["http", "https", "socks4", "socks5"].map((p) => (
                      <option key={p} value={p}>
                        {p}
                      </option>
                    ))}
                  </Select>
                </Field>
                {form.format === "html_table" ? (
                  <>
                    <Field label="IP 列索引">
                      <Input type="number" min={0} value={form.host_col} onChange={(e) => setForm({ ...form, host_col: Number(e.target.value) })} />
                    </Field>
                    <Field label="端口列索引">
                      <Input type="number" min={0} value={form.port_col} onChange={(e) => setForm({ ...form, port_col: Number(e.target.value) })} />
                    </Field>
                  </>
                ) : null}
              </div>
              <Field label="URL 列表（每行一个）">
                <Textarea
                  required
                  rows={4}
                  value={form.urls}
                  onChange={(e) => setForm({ ...form, urls: e.target.value })}
                  placeholder={"https://example.com/proxies.txt\nhttps://example.com/list.json"}
                />
              </Field>
              <div className="flex flex-wrap gap-4 text-sm">
                <label className="flex items-center gap-2">
                  <input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />
                  默认启用
                </label>
                <label className="flex items-center gap-2">
                  <input type="checkbox" checked={form.fragile} onChange={(e) => setForm({ ...form, fragile: e.target.checked })} />
                  标记为 fragile
                </label>
              </div>
              <div className="flex gap-2">
                <Button type="submit">{editing ? "保存修改" : "创建"}</Button>
                <Button type="button" variant="secondary" onClick={() => { setShowForm(false); setEditing(null); }}>
                  取消
                </Button>
              </div>
            </form>
          </CardContent>
        </Card>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle>采集器列表</CardTitle>
          <Input className="max-w-xs" placeholder="搜索源名称" value={q} onChange={(e) => setQ(e.target.value)} />
        </CardHeader>
        <CardContent className="space-y-3 anim-stagger">
          {loading ? <div className="text-sm text-muted-foreground">加载中...</div> : null}
          {!loading && filtered.length === 0 ? <div className="text-sm text-muted-foreground">无匹配源</div> : null}
          {filtered.map((item) => (
            <div key={item.name} className="row-hover rounded-2xl border border-white/50 bg-white/50 p-3 dark:border-white/10 dark:bg-white/5">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div>
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{item.name}</span>
                    <StatusBadge status={item.enabled ? "enabled" : "disabled"} />
                    {item.builtin ? <StatusBadge status="内置" /> : <StatusBadge status="自定义" />}
                    {item.fragile ? <StatusBadge status="fragile" /> : null}
                    {item.format ? <span className="soft-pill">{item.format}</span> : null}
                  </div>
                  <div className="mt-1 break-all text-xs text-muted-foreground">
                    {item.protocol} · {item.url_hint || "-"}
                  </div>
                </div>
                <div className="text-xs text-muted-foreground">最近运行：{formatTime(item.last_run_at)}</div>
              </div>
              <div className="mt-2 grid gap-1 text-xs text-muted-foreground sm:grid-cols-3">
                <div>本次新增：{item.last_ok}</div>
                <div>累计成功：{item.total_ok}</div>
                <div>累计失败：{item.total_fail}</div>
              </div>
              {item.last_error ? <div className="mt-2 text-xs text-danger">{item.last_error}</div> : null}
              <div className="mt-3 flex flex-wrap gap-2">
                <Button size="sm" variant="secondary" disabled={busy === item.name} onClick={() => void toggle(item.name)}>
                  {item.enabled ? "禁用" : "启用"}
                </Button>
                <Button size="sm" disabled={busy === item.name} onClick={() => void runOne(item.name)}>
                  立即运行
                </Button>
                {!item.builtin ? (
                  <>
                    <Button size="sm" variant="secondary" onClick={() => startEdit(item)}>
                      编辑
                    </Button>
                    <Button size="sm" variant="danger" onClick={() => setPendingDelete(item.name)}>
                      删除
                    </Button>
                  </>
                ) : null}
              </div>
            </div>
          ))}
        </CardContent>
      </Card>

      <ConfirmDialog
        open={Boolean(pendingDelete)}
        title="删除采集源"
        description={pendingDelete ? `确定删除自定义源「${pendingDelete}」吗？` : undefined}
        confirmText="删除"
        danger
        loading={deleting}
        onCancel={() => !deleting && setPendingDelete(null)}
        onConfirm={() => void confirmDelete()}
      />
    </div>
  );
}
