import { useCallback, useEffect, useState } from "react";
import { endpoints } from "@/api";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { PageHeader } from "@/components/PageHeader";
import { StatusBadge } from "@/components/StatusBadge";
import { useSse } from "@/components/SseProvider";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Select } from "@/components/ui/input";
import { useToast } from "@/hooks/useToast";
import { formatLatency } from "@/lib/utils";

type ProxyItem = {
  addr: string;
  host?: string;
  port?: number;
  protocol: string;
  source: string;
  score: number;
  latency_ms: number;
  region: string;
  validated: boolean;
  fail_count?: number;
  last_check?: string;
  created_at?: string;
};

type ListResult = {
  items: ProxyItem[];
  total: number;
  page: number;
  size: number;
};

type RowFeedback = {
  tone: "ok" | "fail" | "info";
  text: string;
};

function schemeFromProtocol(p?: string) {
  const v = (p || "http").toLowerCase();
  if (v === "socks5" || v === "socks4" || v === "https" || v === "http") return v;
  if (v.startsWith("socks")) return "socks5";
  return "http";
}

function connURL(item: ProxyItem) {
  const scheme = schemeFromProtocol(item.protocol);
  return `${scheme}://${item.addr}`;
}

function fmtTime(v?: string) {
  if (!v) return "-";
  try {
    const d = new Date(v);
    if (Number.isNaN(d.getTime())) return v;
    return d.toLocaleString();
  } catch {
    return v;
  }
}

export function ProxiesPage() {
  const { toast } = useToast();
  const [data, setData] = useState<ListResult>({ items: [], total: 0, page: 1, size: 20 });
  const [q, setQ] = useState("");
  const [proto, setProto] = useState("");
  const [source, setSource] = useState("");
  const [onlyOK, setOnlyOK] = useState(false);
  const [region, setRegion] = useState("");
  const [groupByCountry, setGroupByCountry] = useState(false);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const [testing, setTesting] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [pendingDelete, setPendingDelete] = useState<string | null>(null);
  const [rowFeedback, setRowFeedback] = useState<Record<string, RowFeedback>>({});
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});

  const setFeedback = (addr: string, fb: RowFeedback) => {
    setRowFeedback((prev) => ({ ...prev, [addr]: fb }));
    window.setTimeout(() => {
      setRowFeedback((prev) => {
        if (prev[addr]?.text !== fb.text) return prev;
        const next = { ...prev };
        delete next[addr];
        return next;
      });
    }, 5000);
  };

  const copyText = async (text: string, label = "已复制") => {
    try {
      await navigator.clipboard?.writeText(text);
      toast(label, "success");
    } catch {
      toast("复制失败", "error");
    }
  };

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const result = await endpoints.proxies.list({
        page,
        size: groupByCountry ? 100 : 20,
        q: q || undefined,
        proto: proto || undefined,
        source: source || undefined,
        region: region || undefined,
        only_ok: onlyOK || undefined,
      });
      setData({
        items: result?.items || [],
        total: result?.total || 0,
        page: result?.page || page,
        size: result?.size || 20,
      });
    } catch (error) {
      toast(error instanceof Error ? error.message : "加载失败", "error");
    } finally {
      setLoading(false);
    }
  }, [groupByCountry, onlyOK, page, proto, q, region, source, toast]);

  useEffect(() => {
    void load();
  }, [load]);

  useSse(() => {
    void load();
  }, [load]);

  const testOne = async (addr: string) => {
    if (testing || deleting) return;
    setTesting(addr);
    setFeedback(addr, { tone: "info", text: "测试中..." });
    try {
      const result = await endpoints.proxies.test(addr);
      if (result.ok) {
        const latency = result.proxy?.latency_ms;
        const msg = latency ? `可用 · ${latency} ms` : "可用";
        setFeedback(addr, { tone: "ok", text: msg });
        toast(`${addr} 测试通过${latency ? `（${latency} ms）` : ""}`, "success");
      } else {
        const err = result.error || "不可用";
        setFeedback(addr, { tone: "fail", text: err });
        toast(`${addr} 测试失败：${err}`, "error");
      }
      await load();
    } catch (error) {
      const msg = error instanceof Error ? error.message : "测试失败";
      setFeedback(addr, { tone: "fail", text: msg });
      toast(msg, "error");
    } finally {
      setTesting(null);
    }
  };

  const confirmDelete = async () => {
    const addr = pendingDelete;
    if (!addr) return;
    setDeleting(addr);
    setFeedback(addr, { tone: "info", text: "删除中..." });
    try {
      await endpoints.proxies.remove(addr);
      setFeedback(addr, { tone: "ok", text: "已删除" });
      toast(`已删除 ${addr}`, "success");
      setPendingDelete(null);
      await load();
    } catch (error) {
      const msg = error instanceof Error ? error.message : "删除失败";
      setFeedback(addr, { tone: "fail", text: msg });
      toast(msg, "error");
    } finally {
      setDeleting(null);
    }
  };

  const totalPages = Math.max(1, Math.ceil((data.total || 0) / (data.size || 20)));

  const countries = Array.from(
    new Set((data.items || []).map((i) => i.region || "unknown").filter(Boolean)),
  ).sort();

  const grouped = groupByCountry
    ? countries.map((c) => ({
        country: c,
        items: (data.items || []).filter((i) => (i.region || "unknown") === c),
      }))
    : [{ country: "", items: data.items || [] }];

  return (
    <div className="anim-fade-up">
      <PageHeader title="免费代理池" description="完整连接串 · 复制 · 详情展开 · 按国家筛选" />
      <Card>
        <CardHeader>
          <CardTitle>筛选</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="mb-4 grid gap-2 md:grid-cols-3 lg:grid-cols-6">
            <Input placeholder="搜索 host:port / 源" value={q} onChange={(e) => { setPage(1); setQ(e.target.value); }} />
            <Select value={proto} onChange={(e) => { setPage(1); setProto(e.target.value); }}>
              <option value="">全部协议</option>
              <option value="http">http</option>
              <option value="https">https</option>
              <option value="socks4">socks4</option>
              <option value="socks5">socks5</option>
            </Select>
            <Input placeholder="来源 source" value={source} onChange={(e) => { setPage(1); setSource(e.target.value); }} />
            <Input placeholder="国家/地区 region" value={region} onChange={(e) => { setPage(1); setRegion(e.target.value); }} />
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={onlyOK} onChange={(e) => { setPage(1); setOnlyOK(e.target.checked); }} />
              仅已验证
            </label>
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={groupByCountry} onChange={(e) => { setPage(1); setGroupByCountry(e.target.checked); }} />
              按国家分组
            </label>
          </div>
          <div className="mb-3 flex flex-wrap gap-2">
            <Button variant="secondary" onClick={() => void load()} disabled={loading}>
              {loading ? "刷新中..." : "刷新"}
            </Button>
            <Button
              variant="secondary"
              onClick={() => {
                const url = endpoints.proxies.exportUrl({
                  format: "txt",
                  only_ok: onlyOK || undefined,
                  proto: proto || undefined,
                  region: region || undefined,
                  source: source || undefined,
                  q: q || undefined,
                });
                window.open(url, "_blank");
              }}
            >
              导出 txt
            </Button>
            <Button
              variant="secondary"
              onClick={() => {
                const url = endpoints.proxies.exportUrl({
                  format: "url",
                  only_ok: onlyOK || undefined,
                  proto: proto || undefined,
                });
                window.open(url, "_blank");
              }}
            >
              导出 URL
            </Button>
            <Button
              variant="secondary"
              onClick={async () => {
                try {
                  const r = await endpoints.proxies.purge({ only_invalid: true, dry_run: true });
                  if (!window.confirm(`预览将删除 ${r.matched} 条未验证代理，确认执行？`)) return;
                  const r2 = await endpoints.proxies.purge({ only_invalid: true, dry_run: false });
                  toast(`已删除 ${r2.deleted} 条`, "success");
                  void load();
                } catch (e) {
                  toast(e instanceof Error ? e.message : "清理失败", "error");
                }
              }}
            >
              清理未验证
            </Button>
          </div>
          <div className="overflow-x-auto rounded-2xl border border-white/50 dark:border-white/10">
            <table className="min-w-full text-left text-sm">
              <thead className="bg-white/40 text-xs text-muted-foreground dark:bg-white/5">
                <tr>
                  <th className="px-3 py-2.5">地址</th>
                  <th className="px-3 py-2.5">协议</th>
                  <th className="px-3 py-2.5">来源</th>
                  <th className="px-3 py-2.5">国家</th>
                  <th className="px-3 py-2.5">评分</th>
                  <th className="px-3 py-2.5">延迟</th>
                  <th className="px-3 py-2.5">状态</th>
                  <th className="px-3 py-2.5">操作</th>
                </tr>
              </thead>
              <tbody>
                {loading ? (
                  <tr>
                    <td colSpan={8} className="px-3 py-10 text-center text-muted-foreground">
                      加载中...
                    </td>
                  </tr>
                ) : (data.items || []).length === 0 ? (
                  <tr>
                    <td colSpan={8} className="px-3 py-10 text-center text-muted-foreground">
                      暂无代理，可到「采集源」手动运行或等待调度
                    </td>
                  </tr>
                ) : (
                  grouped.flatMap((g) => {
                    const rows = g.items.flatMap((item) => {
                      const busy = testing === item.addr || deleting === item.addr;
                      const fb = rowFeedback[item.addr];
                      const url = connURL(item);
                      const open = Boolean(expanded[item.addr]);
                      const main = (
                        <tr key={item.addr} className="row-hover border-t border-white/40 dark:border-white/5">
                          <td className="px-3 py-2.5">
                            <div className="flex flex-wrap items-center gap-1.5">
                              <span className="font-mono text-xs font-medium">{item.addr}</span>
                              <button
                                type="button"
                                className="rounded-full border border-white/60 bg-white/50 px-2 py-0.5 text-[10px] hover:bg-white/80 dark:border-white/10 dark:bg-white/10"
                                onClick={() => void copyText(item.addr, "已复制 host:port")}
                              >
                                复制
                              </button>
                            </div>
                            <div className="mt-1 flex flex-wrap items-center gap-1.5">
                              <span className="font-mono text-[11px] text-muted-foreground">{url}</span>
                              <button
                                type="button"
                                className="rounded-full border border-white/60 bg-white/40 px-2 py-0.5 text-[10px] hover:bg-white/80 dark:border-white/10 dark:bg-white/10"
                                onClick={() => void copyText(url, "已复制连接串")}
                              >
                                连接串
                              </button>
                            </div>
                            {fb ? (
                              <div
                                className={
                                  fb.tone === "ok"
                                    ? "mt-1 text-[11px] font-medium text-success"
                                    : fb.tone === "fail"
                                      ? "mt-1 text-[11px] font-medium text-danger"
                                      : "mt-1 text-[11px] text-muted-foreground"
                                }
                              >
                                {fb.text}
                              </div>
                            ) : null}
                          </td>
                          <td className="px-3 py-2.5">{item.protocol}</td>
                          <td className="px-3 py-2.5">{item.source}</td>
                          <td className="px-3 py-2.5">
                            <span className="soft-pill">{item.region || "unknown"}</span>
                          </td>
                          <td className="px-3 py-2.5 tabular-nums">{item.score}</td>
                          <td className="px-3 py-2.5 tabular-nums">{formatLatency(item.latency_ms)}</td>
                          <td className="px-3 py-2.5">
                            <StatusBadge status={item.validated ? "available" : "pending"} />
                          </td>
                          <td className="px-3 py-2.5">
                            <div className="flex flex-wrap gap-1.5">
                              <Button
                                size="sm"
                                variant="secondary"
                                onClick={() => setExpanded((prev) => ({ ...prev, [item.addr]: !open }))}
                              >
                                {open ? "收起" : "详情"}
                              </Button>
                              <Button size="sm" variant="secondary" disabled={busy} onClick={() => void testOne(item.addr)}>
                                {testing === item.addr ? "测试中..." : "测试"}
                              </Button>
                              <Button
                                size="sm"
                                variant="secondary"
                                disabled={busy}
                                onClick={async () => {
                                  try {
                                    await endpoints.blacklist.add({ addr: item.addr, reason: "manual" });
                                    toast(`已拉黑 ${item.addr}`, "success");
                                  } catch (e) {
                                    toast(e instanceof Error ? e.message : "拉黑失败", "error");
                                  }
                                }}
                              >
                                拉黑
                              </Button>
                              <Button size="sm" variant="danger" disabled={busy} onClick={() => setPendingDelete(item.addr)}>
                                {deleting === item.addr ? "删除中..." : "删除"}
                              </Button>
                            </div>
                          </td>
                        </tr>
                      );
                      if (!open) return [main];
                      const detail = (
                        <tr key={`${item.addr}-detail`} className="border-t border-white/20 bg-white/30 dark:border-white/5 dark:bg-white/[0.03]">
                          <td colSpan={8} className="px-4 py-3">
                            <div className="grid gap-2 text-xs text-muted-foreground sm:grid-cols-2 lg:grid-cols-4">
                              <div><span className="text-foreground/70">Host</span> · <span className="font-mono text-foreground">{item.host || item.addr.split(":")[0]}</span></div>
                              <div><span className="text-foreground/70">Port</span> · <span className="font-mono text-foreground">{item.port ?? item.addr.split(":")[1] ?? "-"}</span></div>
                              <div><span className="text-foreground/70">Protocol</span> · {item.protocol}</div>
                              <div><span className="text-foreground/70">Source</span> · {item.source}</div>
                              <div><span className="text-foreground/70">Region</span> · {item.region || "unknown"}</div>
                              <div><span className="text-foreground/70">Score</span> · {item.score}</div>
                              <div><span className="text-foreground/70">Fail</span> · {item.fail_count ?? 0}</div>
                              <div><span className="text-foreground/70">Latency</span> · {formatLatency(item.latency_ms)}</div>
                              <div><span className="text-foreground/70">Last check</span> · {fmtTime(item.last_check)}</div>
                              <div><span className="text-foreground/70">Created</span> · {fmtTime(item.created_at)}</div>
                              <div className="sm:col-span-2"><span className="text-foreground/70">URL</span> · <span className="font-mono text-foreground">{url}</span></div>
                            </div>
                          </td>
                        </tr>
                      );
                      return [main, detail];
                    });
                    if (groupByCountry && g.country) {
                      return [
                        <tr key={`g-${g.country}`} className="border-t border-white/40 bg-white/30 dark:border-white/5 dark:bg-white/5">
                          <td colSpan={8} className="px-3 py-2 text-xs font-semibold">
                            {g.country} · {g.items.length} 个
                          </td>
                        </tr>,
                        ...rows,
                      ];
                    }
                    return rows;
                  })
                )}
              </tbody>
            </table>
          </div>
          <div className="mt-3 flex items-center justify-between text-sm text-muted-foreground">
            <div>共 {data.total} 条</div>
            <div className="flex gap-2">
              <Button size="sm" variant="secondary" disabled={page <= 1 || loading} onClick={() => setPage((p) => Math.max(1, p - 1))}>
                上一页
              </Button>
              <span className="px-2 py-1">
                {page} / {totalPages}
              </span>
              <Button size="sm" variant="secondary" disabled={page >= totalPages || loading} onClick={() => setPage((p) => p + 1)}>
                下一页
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <ConfirmDialog
        open={Boolean(pendingDelete)}
        title="删除代理"
        description={pendingDelete ? `确定从代理池中移除 ${pendingDelete} 吗？此操作不可撤销。` : undefined}
        confirmText="删除"
        cancelText="取消"
        danger
        loading={Boolean(deleting)}
        onCancel={() => {
          if (!deleting) setPendingDelete(null);
        }}
        onConfirm={() => void confirmDelete()}
      />
    </div>
  );
}
