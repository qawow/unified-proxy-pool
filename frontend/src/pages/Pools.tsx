import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";
import { endpoints } from "@/api";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { PageHeader } from "@/components/PageHeader";
import { StatusBadge } from "@/components/StatusBadge";
import { useSse } from "@/components/SseProvider";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { useToast } from "@/hooks/useToast";
import { formatLatency, formatTime } from "@/lib/utils";
import type { PoolMember, PoolMemberView, ProxyPool, StrategyAdvanced, StrategyTemplate } from "@/types";

const STRATEGY_OPTIONS = [
  {
    value: "round_robin",
    label: "轮询调度",
    desc: "按顺序轮流使用节点，适合流量均衡；可配合权重放大某节点占比。",
  },
  {
    value: "lowest_latency",
    label: "最低延迟",
    desc: "自动选择延迟最低的节点（url-test），并持续健康探测。",
  },
  {
    value: "failover",
    label: "故障转移",
    desc: "优先使用列表靠前的节点，失败后依次切换（fallback）。",
  },
  {
    value: "sticky",
    label: "会话粘滞",
    desc: "同一客户端尽量固定到同一节点（sticky-sessions），减少会话中断。",
  },
] as const;

const SOURCE_LABELS: Record<string, string> = {
  manual: "手动节点",
  subscription: "订阅节点",
  free_proxy: "免费代理",
};

const CUSTOM_JSON_PLACEHOLDER = `{
  "display_name": "我的策略",
  "template": "custom",
  "group_type": "url-test",
  "lb_strategy": "",
  "health_url": "https://www.gstatic.com/generate_204",
  "interval": 120,
  "tolerance": 50,
  "lazy": true,
  "disable_health": false,
  "extra": {
    "expected-status": 204
  }
}`;

function strategyLabel(pool: { strategy?: string; strategy_label?: string; strategy_advanced_json?: string }) {
  if (pool.strategy_label?.trim()) return pool.strategy_label.trim();
  try {
    const adv = JSON.parse(pool.strategy_advanced_json || "{}") as StrategyAdvanced;
    if (adv.display_name?.trim()) return adv.display_name.trim();
  } catch {
    /* ignore */
  }
  return STRATEGY_OPTIONS.find((s) => s.value === pool.strategy)?.label || pool.strategy || "轮询调度";
}

function strategyDesc(value?: string) {
  return STRATEGY_OPTIONS.find((s) => s.value === value)?.desc || "";
}

function parseAdvanced(raw?: string): StrategyAdvanced {
  try {
    return (JSON.parse(raw || "{}") as StrategyAdvanced) || {};
  } catch {
    return {};
  }
}

const emptyForm = {
  id: 0,
  name: "",
  auth_username: "",
  auth_password_secret: "",
  strategy: "round_robin",
  strategy_label: "",
  strategy_advanced_json: "{}",
  failover_enabled: true,
  enabled: true,
};

export function PoolsPage() {
  const { toast } = useToast();
  const [pools, setPools] = useState<ProxyPool[]>([]);
  const [candidates, setCandidates] = useState<PoolMemberView[]>([]);
  const [templates, setTemplates] = useState<StrategyTemplate[]>([]);
  const [selected, setSelected] = useState<Map<string, number>>(new Map());
  const [form, setForm] = useState(emptyForm);
  const [search, setSearch] = useState("");
  const [sourceFilter, setSourceFilter] = useState("");
  const [protocolFilter, setProtocolFilter] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [showJsonEditor, setShowJsonEditor] = useState(false);
  const [jsonDraft, setJsonDraft] = useState("{}");
  const [jsonError, setJsonError] = useState("");
  const [pendingDelete, setPendingDelete] = useState<number | null>(null);
  const [deleting, setDeleting] = useState(false);

  const advanced = useMemo(() => parseAdvanced(form.strategy_advanced_json), [form.strategy_advanced_json]);

  const keyOf = (sourceType: string, sourceNodeID: number) => `${sourceType}:${sourceNodeID}`;

  const load = useCallback(async () => {
    try {
      const [poolList, candList, tpls] = await Promise.all([
        endpoints.pools.list(),
        endpoints.pools.candidates(),
        endpoints.pools.strategyTemplates().catch(() => [] as StrategyTemplate[]),
      ]);
      setPools(poolList || []);
      setCandidates(candList || []);
      setTemplates(tpls || []);
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

  const filteredCandidates = useMemo(() => {
    return candidates.filter((item) => {
      if (sourceFilter && item.source_type !== sourceFilter) return false;
      if (protocolFilter && (item.protocol || "").toLowerCase() !== protocolFilter) return false;
      const q = search.trim().toLowerCase();
      if (!q) return true;
      return (
        item.display_name.toLowerCase().includes(q) ||
        item.server.toLowerCase().includes(q) ||
        item.protocol.toLowerCase().includes(q) ||
        (SOURCE_LABELS[item.source_type] || item.source_type).includes(q)
      );
    });
  }, [candidates, protocolFilter, search, sourceFilter]);

  const selectedCount = selected.size;
  const selectedWeightSum = useMemo(() => {
    let sum = 0;
    selected.forEach((w) => {
      sum += w;
    });
    return sum;
  }, [selected]);

  const patchAdvanced = (patch: Partial<StrategyAdvanced>, also?: Partial<typeof emptyForm>) => {
    setForm((f) => {
      const cur = parseAdvanced(f.strategy_advanced_json);
      const next: StrategyAdvanced = { ...cur, ...patch };
      const raw = JSON.stringify(next);
      queueMicrotask(() => {
        setJsonDraft(JSON.stringify(next, null, 2));
        setJsonError("");
      });
      return { ...f, ...also, strategy_advanced_json: raw };
    });
  };

  const applyTemplate = (templateId: string) => {
    const tpl = templates.find((t) => t.id === templateId);
    const base: StrategyAdvanced = {
      ...(tpl?.defaults || {}),
      template: templateId,
      display_name: form.strategy_label || tpl?.defaults?.display_name || tpl?.name || advanced.display_name,
    };
    const raw = JSON.stringify(base);
    setForm((f) => ({
      ...f,
      strategy_advanced_json: raw,
      strategy_label: f.strategy_label || base.display_name || "",
    }));
    setJsonDraft(JSON.stringify(base, null, 2));
    setJsonError("");
    if (templateId === "custom") setShowJsonEditor(true);
  };

  const applyJsonDraft = () => {
    try {
      const parsed = JSON.parse(jsonDraft || "{}") as StrategyAdvanced;
      if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
        throw new Error("必须是 JSON 对象");
      }
      const raw = JSON.stringify(parsed);
      setForm((f) => ({
        ...f,
        strategy_advanced_json: raw,
        strategy_label: f.strategy_label || parsed.display_name || "",
      }));
      setJsonError("");
      toast("已应用自定义 JSON", "success");
    } catch (e) {
      setJsonError(e instanceof Error ? e.message : "JSON 无效");
    }
  };

  const resetForm = () => {
    setForm(emptyForm);
    setSelected(new Map());
    setShowAdvanced(false);
    setShowJsonEditor(false);
    setJsonDraft("{}");
    setJsonError("");
  };

  const editPool = async (pool: ProxyPool) => {
    const advRaw = pool.strategy_advanced_json || "{}";
    setForm({
      id: pool.id,
      name: pool.name,
      auth_username: pool.auth_username,
      auth_password_secret: pool.auth_password_secret || "",
      strategy: pool.strategy || "round_robin",
      strategy_label: pool.strategy_label || "",
      strategy_advanced_json: advRaw,
      failover_enabled: pool.failover_enabled,
      enabled: pool.enabled,
    });
    setJsonDraft(() => {
      try {
        return JSON.stringify(JSON.parse(advRaw), null, 2);
      } catch {
        return advRaw;
      }
    });
    setShowAdvanced(true);
    setShowJsonEditor(parseAdvanced(advRaw).template === "custom");
    try {
      const members = await endpoints.pools.members(pool.id);
      const list = Array.isArray(members)
        ? members
        : (members as { members?: PoolMember[] }).members || [];
      const map = new Map<string, number>();
      list.forEach((m) => {
        if (m.enabled !== false) map.set(keyOf(m.source_type, m.source_node_id), m.weight || 1);
      });
      setSelected(map);
    } catch (error) {
      toast(error instanceof Error ? error.message : "加载成员失败", "error");
    }
  };

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setSaving(true);
    try {
      // ensure json draft synced if editor open
      let advancedJSON = form.strategy_advanced_json || "{}";
      if (showJsonEditor) {
        try {
          const parsed = JSON.parse(jsonDraft || "{}");
          advancedJSON = JSON.stringify(parsed);
        } catch {
          toast("高级 JSON 无效，请先修正", "error");
          setSaving(false);
          return;
        }
      }
      const payload = {
        name: form.name,
        auth_username: form.auth_username,
        auth_password_secret: form.auth_password_secret,
        strategy: form.strategy,
        strategy_label: form.strategy_label,
        strategy_advanced_json: advancedJSON,
        failover_enabled: form.failover_enabled,
        enabled: form.enabled,
      };
      let saved: ProxyPool;
      if (form.id) {
        saved = await endpoints.pools.update(form.id, payload);
      } else {
        saved = await endpoints.pools.create(payload);
      }
      const members = Array.from(selected.entries()).map(([key, weight]) => {
        const idx = key.indexOf(":");
        const source_type = idx >= 0 ? key.slice(0, idx) : key;
        const source_node_id = idx >= 0 ? Number(key.slice(idx + 1)) : 0;
        return {
          source_type,
          source_node_id,
          enabled: true,
          weight,
        };
      });
      await endpoints.pools.updateMembers(saved.id, members);
      toast("代理池已保存", "success");
      resetForm();
      await load();
    } catch (error) {
      toast(error instanceof Error ? error.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  };

  const poolAction = async (id: number, action: "toggle" | "publish" | "delete") => {
    try {
      if (action === "delete") {
        setPendingDelete(id);
        return;
      }
      await (action === "publish" ? endpoints.pools.publish(id) : endpoints.pools.toggle(id));
      await load();
      toast(action === "publish" ? "已发布" : "操作成功", "success");
    } catch (error) {
      toast(error instanceof Error ? error.message : "操作失败", "error");
    }
  };

  const confirmDelete = async () => {
    if (pendingDelete == null) return;
    setDeleting(true);
    try {
      await endpoints.pools.remove(pendingDelete);
      toast("已删除代理池", "success");
      setPendingDelete(null);
      if (form.id === pendingDelete) resetForm();
      await load();
    } catch (error) {
      toast(error instanceof Error ? error.message : "删除失败", "error");
    } finally {
      setDeleting(false);
    }
  };

  const currentTemplate = advanced.template || "";

  return (
    <div>
      <PageHeader title="出口池" description="四种基础调度 + 可自定义命名/模板/JSON 的高级策略" />
      <div className="grid gap-4 xl:grid-cols-[420px_1fr]">
        <Card>
          <CardHeader>
            <CardTitle>{form.id ? "编辑代理池" : "新增代理池"}</CardTitle>
          </CardHeader>
          <CardContent>
            <form className="space-y-3" onSubmit={onSubmit}>
              <Field label="名称">
                <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required placeholder="例如：办公出口" />
              </Field>
              <Field label="用户名（唯一标识）">
                <Input value={form.auth_username} onChange={(e) => setForm({ ...form, auth_username: e.target.value })} required />
              </Field>
              <Field label="密码">
                <Input
                  type="password"
                  value={form.auth_password_secret}
                  onChange={(e) => setForm({ ...form, auth_password_secret: e.target.value })}
                  required={!form.id}
                  placeholder={form.id ? "留空则不修改" : ""}
                />
              </Field>

              <Field label="基础调度策略">
                <Select
                  value={form.strategy}
                  onChange={(e) => {
                    setForm({ ...form, strategy: e.target.value });
                    setShowAdvanced(true);
                  }}
                >
                  {STRATEGY_OPTIONS.map((opt) => (
                    <option key={opt.value} value={opt.value}>
                      {opt.label}
                    </option>
                  ))}
                </Select>
              </Field>
              <div className="rounded-2xl bg-sky-50/80 px-3 py-2 text-xs leading-relaxed text-sky-900 dark:bg-sky-950/40 dark:text-sky-100">
                {strategyDesc(form.strategy)}
              </div>
              <div className="grid grid-cols-2 gap-2">
                {STRATEGY_OPTIONS.map((opt) => (
                  <button
                    key={opt.value}
                    type="button"
                    onClick={() => {
                      setForm({ ...form, strategy: opt.value });
                      setShowAdvanced(true);
                    }}
                    className={`rounded-2xl border px-2.5 py-2 text-left text-xs transition ${
                      form.strategy === opt.value
                        ? "border-sky-400/70 bg-sky-50 text-sky-900 shadow-sm dark:bg-sky-950/50 dark:text-sky-100"
                        : "border-white/50 bg-white/40 hover:bg-white/70 dark:border-white/10 dark:bg-white/5"
                    }`}
                  >
                    <div className="font-semibold">{opt.label}</div>
                    <div className="mt-0.5 opacity-70">{opt.value}</div>
                  </button>
                ))}
              </div>

              <label className="flex items-center gap-2 text-sm">
                <input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />
                启用代理池
              </label>

              {/* 选择策略后展开高级详情 */}
              <div className={`overflow-hidden rounded-2xl border border-white/60 bg-white/40 transition-all dark:border-white/10 dark:bg-white/5 ${showAdvanced ? "anim-fade-up" : ""}`}>
                <button
                  type="button"
                  className="flex w-full items-center justify-between px-3 py-2.5 text-left text-sm font-medium"
                  onClick={() => setShowAdvanced((v) => !v)}
                >
                  <span>
                    策略详情 · {strategyLabel({ strategy: form.strategy, strategy_label: form.strategy_label, strategy_advanced_json: form.strategy_advanced_json })}
                  </span>
                  {showAdvanced ? <ChevronUp className="h-4 w-4" /> : <ChevronDown className="h-4 w-4" />}
                </button>
                {showAdvanced ? (
                  <div className="space-y-3 border-t border-white/50 px-3 py-3 dark:border-white/10">
                    <Field label="策略显示名称（可自定义）">
                      <Input
                        value={form.strategy_label}
                        onChange={(e) => {
                          const v = e.target.value;
                          patchAdvanced({ display_name: v }, { strategy_label: v });
                        }}
                        placeholder="例如：晚高峰低延迟"
                      />
                    </Field>

                    <Field label="高级模板">
                      <Select
                        value={currentTemplate}
                        onChange={(e) => applyTemplate(e.target.value)}
                      >
                        {(templates.length
                          ? templates
                          : [
                              { id: "", name: "跟随基础策略", description: "" },
                              { id: "fast_test", name: "快速测速", description: "" },
                              { id: "stable", name: "稳定优先", description: "" },
                              { id: "hash_sticky", name: "哈希粘滞", description: "" },
                              { id: "manual_select", name: "手动选择", description: "" },
                              { id: "custom", name: "自定义", description: "" },
                            ]
                        ).map((t) => (
                          <option key={t.id || "default"} value={t.id}>
                            {t.name}
                          </option>
                        ))}
                      </Select>
                    </Field>
                    {templates.find((t) => t.id === currentTemplate)?.description ? (
                      <div className="text-xs text-muted-foreground">
                        {templates.find((t) => t.id === currentTemplate)?.description}
                      </div>
                    ) : null}

                    <div className="grid gap-2 sm:grid-cols-2">
                      <Field label="组类型覆盖（可选）">
                        <Select
                          value={advanced.group_type || ""}
                          onChange={(e) => patchAdvanced({ group_type: e.target.value || undefined, template: advanced.template || "custom" })}
                        >
                          <option value="">默认（跟随基础策略）</option>
                          <option value="load-balance">load-balance 负载均衡</option>
                          <option value="url-test">url-test 自动测速</option>
                          <option value="fallback">fallback 故障转移</option>
                          <option value="select">select 手动选择</option>
                        </Select>
                      </Field>
                      <Field label="负载策略（load-balance）">
                        <Select
                          value={advanced.lb_strategy || ""}
                          onChange={(e) => patchAdvanced({ lb_strategy: e.target.value || undefined, template: advanced.template || "custom" })}
                        >
                          <option value="">默认</option>
                          <option value="round-robin">round-robin 轮询</option>
                          <option value="consistent-hashing">consistent-hashing 哈希</option>
                          <option value="sticky-sessions">sticky-sessions 粘滞</option>
                        </Select>
                      </Field>
                    </div>

                    <label className="flex items-start gap-2 text-sm">
                      <input
                        className="mt-1"
                        type="checkbox"
                        checked={form.failover_enabled}
                        onChange={(e) => setForm({ ...form, failover_enabled: e.target.checked })}
                      />
                      <span>
                        <span className="font-medium">基础健康检查开关</span>
                        <span className="mt-0.5 block text-xs text-muted-foreground">对应原「失败自动切换」；可与下方探测参数叠加。</span>
                      </span>
                    </label>

                    <label className="flex items-center gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={Boolean(advanced.disable_health)}
                        onChange={(e) => patchAdvanced({ disable_health: e.target.checked, template: advanced.template || "custom" })}
                      />
                      强制关闭健康探测（高级）
                    </label>

                    <div className="grid gap-2 sm:grid-cols-3">
                      <Field label="探测间隔(秒)">
                        <Input
                          type="number"
                          min={0}
                          placeholder="300"
                          value={advanced.interval ?? ""}
                          onChange={(e) =>
                            patchAdvanced({
                              interval: e.target.value === "" ? undefined : Number(e.target.value),
                              template: advanced.template || "custom",
                            })
                          }
                        />
                      </Field>
                      <Field label="容差 tolerance">
                        <Input
                          type="number"
                          min={0}
                          placeholder="50"
                          value={advanced.tolerance ?? ""}
                          onChange={(e) =>
                            patchAdvanced({
                              tolerance: e.target.value === "" ? undefined : Number(e.target.value),
                              template: advanced.template || "custom",
                            })
                          }
                        />
                      </Field>
                      <Field label="Lazy 懒探测">
                        <Select
                          value={advanced.lazy === undefined ? "" : advanced.lazy ? "1" : "0"}
                          onChange={(e) => {
                            const v = e.target.value;
                            patchAdvanced({
                              lazy: v === "" ? undefined : v === "1",
                              template: advanced.template || "custom",
                            });
                          }}
                        >
                          <option value="">默认</option>
                          <option value="1">开启</option>
                          <option value="0">关闭</option>
                        </Select>
                      </Field>
                    </div>

                    <Field label="健康检查 URL（可选覆盖）">
                      <Input
                        value={advanced.health_url || ""}
                        onChange={(e) =>
                          patchAdvanced({
                            health_url: e.target.value || undefined,
                            template: advanced.template || "custom",
                          })
                        }
                        placeholder="留空则用系统延迟测试 URL"
                      />
                    </Field>

                    <div className="rounded-2xl bg-white/50 px-3 py-2 text-xs text-muted-foreground dark:bg-white/5">
                      <div className="font-medium text-foreground">权重说明</div>
                      <div className="mt-1 leading-relaxed">
                        勾选节点后可设权重 1–32。轮询/粘滞下权重影响选中次数；测速/故障转移以探测结果为主。
                      </div>
                      <div className="mt-2 tabular-nums">
                        已选 <span className="font-semibold text-foreground">{selectedCount}</span> 个
                        {selectedCount > 0 ? (
                          <>
                            ，权重合计 <span className="font-semibold text-foreground">{selectedWeightSum}</span>
                          </>
                        ) : null}
                      </div>
                    </div>

                    <div className="flex items-center justify-between gap-2">
                      <button
                        type="button"
                        className="text-xs font-medium text-sky-700 hover:underline dark:text-sky-300"
                        onClick={() => {
                          setShowJsonEditor((v) => !v);
                          setJsonDraft(() => {
                            try {
                              return JSON.stringify(JSON.parse(form.strategy_advanced_json || "{}"), null, 2);
                            } catch {
                              return form.strategy_advanced_json || "{}";
                            }
                          });
                        }}
                      >
                        {showJsonEditor ? "收起 JSON 编辑器" : "展开 JSON 编辑器（完全自定义）"}
                      </button>
                      <Button
                        type="button"
                        size="sm"
                        variant="secondary"
                        onClick={() => {
                          setForm((f) => ({ ...f, strategy_label: "", strategy_advanced_json: "{}" }));
                          setJsonDraft("{}");
                          setJsonError("");
                        }}
                      >
                        重置高级
                      </Button>
                    </div>

                    {showJsonEditor ? (
                      <div className="space-y-2">
                        <Textarea
                          rows={12}
                          className="font-mono text-xs"
                          value={jsonDraft}
                          onChange={(e) => setJsonDraft(e.target.value)}
                          placeholder={CUSTOM_JSON_PLACEHOLDER}
                        />
                        {jsonError ? <div className="text-xs text-danger">{jsonError}</div> : null}
                        <div className="flex gap-2">
                          <Button type="button" size="sm" onClick={applyJsonDraft}>
                            应用 JSON
                          </Button>
                          <Button
                            type="button"
                            size="sm"
                            variant="secondary"
                            onClick={() => {
                              setJsonDraft(CUSTOM_JSON_PLACEHOLDER);
                              setJsonError("");
                            }}
                          >
                            填入模板示例
                          </Button>
                        </div>
                        <div className="text-[11px] leading-relaxed text-muted-foreground">
                          extra 中的字段会合并进 Mihomo proxy-group（禁止覆盖 name/proxies）。保存前请点「应用 JSON」。
                        </div>
                      </div>
                    ) : null}
                  </div>
                ) : (
                  <div className="border-t border-white/50 px-3 py-2 text-xs text-muted-foreground dark:border-white/10">
                    {form.strategy_label || strategyLabel(form)} · 模板 {currentTemplate || "默认"}
                    {form.failover_enabled ? " · 健康检查开" : ""}
                  </div>
                )}
              </div>

              <div className="space-y-2 rounded-2xl border border-white/60 bg-white/40 p-3 dark:border-white/10 dark:bg-white/5">
                <div className="text-sm font-medium">节点选择器</div>
                <div className="grid gap-2">
                  <Input placeholder="搜索候选节点" value={search} onChange={(e) => setSearch(e.target.value)} />
                  <div className="grid grid-cols-2 gap-2">
                    <Select value={sourceFilter} onChange={(e) => setSourceFilter(e.target.value)}>
                      <option value="">全部来源</option>
                      <option value="manual">手动节点</option>
                      <option value="subscription">订阅节点</option>
                      <option value="free_proxy">免费代理</option>
                    </Select>
                    <Select value={protocolFilter} onChange={(e) => setProtocolFilter(e.target.value)}>
                      <option value="">全部协议</option>
                      {["ss", "trojan", "vmess", "vless", "tuic", "hysteria2", "http", "socks5", "socks4"].map((p) => (
                        <option key={p} value={p}>
                          {p}
                        </option>
                      ))}
                    </Select>
                  </div>
                </div>
                <div className="max-h-64 space-y-2 overflow-y-auto">
                  {filteredCandidates.length === 0 ? (
                    <div className="rounded-2xl border border-dashed border-border/70 p-4 text-center text-xs text-muted-foreground">
                      无匹配候选节点
                    </div>
                  ) : null}
                  {filteredCandidates.map((item) => {
                    const key = keyOf(item.source_type, item.source_node_id);
                    const checked = selected.has(key);
                    return (
                      <label key={key} className="flex items-start gap-2 rounded-2xl border border-white/50 bg-white/50 p-2 text-xs dark:border-white/10 dark:bg-white/5">
                        <input
                          type="checkbox"
                          className="mt-0.5"
                          checked={checked}
                          onChange={(e) => {
                            const next = new Map(selected);
                            if (e.target.checked) next.set(key, 1);
                            else next.delete(key);
                            setSelected(next);
                          }}
                        />
                        <span className="min-w-0 flex-1">
                          <div className="font-medium">{item.display_name}</div>
                          <div className="text-muted-foreground">
                            {SOURCE_LABELS[item.source_type] || item.source_type} · {item.protocol} · {item.server}:{item.port} ·{" "}
                            {formatLatency(item.last_latency_ms)}
                          </div>
                        </span>
                        {checked ? (
                          <div className="flex flex-col items-end gap-0.5">
                            <span className="text-[10px] text-muted-foreground">权重</span>
                            <Input
                              className="h-7 w-14"
                              type="number"
                              min={1}
                              max={32}
                              value={selected.get(key) || 1}
                              onChange={(e) => {
                                const next = new Map(selected);
                                next.set(key, Math.max(1, Math.min(32, Number(e.target.value) || 1)));
                                setSelected(next);
                              }}
                            />
                          </div>
                        ) : null}
                      </label>
                    );
                  })}
                </div>
              </div>

              <div className="flex gap-2">
                <Button type="submit" disabled={saving}>
                  {saving ? "保存中..." : "保存"}
                </Button>
                <Button type="button" variant="secondary" onClick={resetForm}>
                  清空
                </Button>
              </div>
            </form>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>代理池列表</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {loading ? <div className="text-sm text-muted-foreground">加载中...</div> : null}
            {!loading && pools.length === 0 ? <div className="text-sm text-muted-foreground">暂无代理池</div> : null}
            {pools.map((pool) => {
              const adv = parseAdvanced(pool.strategy_advanced_json);
              return (
                <div key={pool.id} className="rounded-2xl border border-white/50 bg-white/50 p-3 dark:border-white/10 dark:bg-white/5">
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div>
                      <div className="font-medium">{pool.name}</div>
                      <div className="text-xs text-muted-foreground">
                        {pool.auth_username} · {strategyLabel(pool)}
                        {adv.template ? ` · 模板 ${adv.template}` : ""}
                        {pool.failover_enabled ? " · 健康检查" : ""}
                      </div>
                    </div>
                    <StatusBadge status={pool.enabled ? pool.last_publish_status || "enabled" : "disabled"} />
                  </div>
                  <div className="mt-2 grid gap-1 text-xs text-muted-foreground sm:grid-cols-2">
                    <div>
                      成员：{pool.current_member_count} / 健康 {pool.current_healthy_count}
                    </div>
                    <div>最近发布：{formatTime(pool.last_published_at)}</div>
                  </div>
                  {pool.last_error ? <div className="mt-2 text-xs text-danger">{pool.last_error}</div> : null}
                  <div className="mt-3 flex flex-wrap gap-2">
                    <Button size="sm" variant="secondary" onClick={() => void editPool(pool)}>
                      编辑
                    </Button>
                    <Button size="sm" variant="secondary" onClick={() => void poolAction(pool.id, "publish")}>
                      发布
                    </Button>
                    <Button size="sm" variant="secondary" onClick={() => void poolAction(pool.id, "toggle")}>
                      {pool.enabled ? "禁用" : "启用"}
                    </Button>
                    <Button size="sm" variant="danger" onClick={() => void poolAction(pool.id, "delete")}>
                      删除
                    </Button>
                  </div>
                </div>
              );
            })}
          </CardContent>
        </Card>
      </div>

      <ConfirmDialog
        open={pendingDelete != null}
        title="删除代理池"
        description={
          pendingDelete != null
            ? `确定删除代理池「${pools.find((p) => p.id === pendingDelete)?.name || pendingDelete}」吗？成员配置将一并移除。`
            : undefined
        }
        confirmText="删除"
        cancelText="取消"
        danger
        loading={deleting}
        onCancel={() => {
          if (!deleting) setPendingDelete(null);
        }}
        onConfirm={() => void confirmDelete()}
      />
    </div>
  );
}
