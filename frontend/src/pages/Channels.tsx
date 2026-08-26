import { useCallback, useEffect, useRef, useState } from "react";
import { endpoints } from "@/api";
import { PageHeader } from "@/components/PageHeader";
import { useSse } from "@/components/SseProvider";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Select } from "@/components/ui/input";
import { useToast } from "@/hooks/useToast";
import type { Channel, ChannelAllow, ChannelBan, ChannelLog, ChannelRule } from "@/types";

/** 封禁原因转成人话。后端给的是稳定的机器标识，展示时再翻译。 */
function describeReason(reason: string): string {
  const reported = reason.endsWith("_reported");
  const base = reported ? reason.slice(0, -"_reported".length) : reason;
  let text: string;
  if (base.startsWith("status_")) {
    text = `返回 ${base.slice("status_".length)}`;
  } else if (base === "consecutive_fails") {
    text = "连续失败";
  } else if (base === "fail_rate") {
    text = "失败率超标";
  } else if (base === "timeouts") {
    text = "多次超时";
  } else {
    text = base || "未知";
  }
  return reported ? `${text}（调用方上报）` : text;
}

function remainingText(until: string): string {
  const ms = new Date(until).getTime() - Date.now();
  if (Number.isNaN(ms) || ms <= 0) return "即将解封";
  const sec = Math.round(ms / 1000);
  if (sec < 60) return `${sec} 秒`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min} 分 ${sec % 60} 秒`;
  return `${Math.floor(min / 60)} 小时 ${min % 60} 分`;
}

function percent(v: number): string {
  return `${Math.round(v * 1000) / 10}%`;
}

export function ChannelsPage() {
  const [items, setItems] = useState<Channel[]>([]);
  const [loading, setLoading] = useState(true);
  const [query, setQuery] = useState("");
  const [onlyBanned, setOnlyBanned] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [bans, setBans] = useState<Record<string, ChannelBan[]>>({});
  const [logs, setLogs] = useState<ChannelLog[]>([]);
  const [allows, setAllows] = useState<ChannelAllow[]>([]);
  const [rules, setRules] = useState<ChannelRule[]>([]);
  const [ruleForm, setRuleForm] = useState({
    name: "",
    channel: "",
    kind: "status",
    statuses: "503,401",
    threshold: "3",
    rate: "0.6",
    match: "",
    ttl_sec: "",
  });
  const [busy, setBusy] = useState<string | null>(null);
  const logRef = useRef<HTMLDivElement>(null);
  const { toast } = useToast();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [list, logItems, allowItems, ruleItems] = await Promise.all([
        endpoints.channels.list({ q: query, onlyBanned }),
        endpoints.channels.logs({ channel: expanded || undefined, limit: 200 }).catch(() => [] as ChannelLog[]),
        endpoints.channels.allowlist().catch(() => [] as ChannelAllow[]),
        endpoints.channels.rules().catch(() => [] as ChannelRule[]),
      ]);
      setItems(list);
      setLogs(logItems);
      setAllows(allowItems);
      setRules(ruleItems);
    } catch (e) {
      toast(e instanceof Error ? e.message : "加载渠道失败", "error");
    } finally {
      setLoading(false);
    }
  }, [query, onlyBanned, expanded, toast]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (logRef.current) {
      logRef.current.scrollTop = logRef.current.scrollHeight;
    }
  }, [logs]);

  const loadBans = useCallback(
    async (channel: string) => {
      try {
        const list = await endpoints.channels.bans(channel);
        setBans((prev) => ({ ...prev, [channel]: list }));
      } catch (e) {
        toast(e instanceof Error ? e.message : "加载封禁明细失败", "error");
      }
    },
    [toast],
  );

  // 封禁/解封是后台自动发生的，靠 SSE 推送刷新，避免用户盯着过期数据。
  useSse((evt) => {
    if (evt.type === "channel_ban" || evt.type === "channel_unban") {
      void load();
      const channel = (evt.payload as { channel?: string } | undefined)?.channel;
      if (channel && expanded === channel) void loadBans(channel);
    }
  });

  const toggle = async (channel: string) => {
    if (expanded === channel) {
      setExpanded(null);
      return;
    }
    setExpanded(channel);
    await loadBans(channel);
  };

  const unban = async (channel: string, addr: string) => {
    setBusy(`${channel}|${addr}`);
    try {
      await endpoints.channels.unban(channel, addr);
      toast(`已解封 ${addr}`, "success");
      await Promise.all([loadBans(channel), load()]);
    } catch (e) {
      toast(e instanceof Error ? e.message : "解封失败", "error");
    } finally {
      setBusy(null);
    }
  };

  const reset = async (channel: string) => {
    if (!window.confirm(`清空渠道「${channel}」的全部封禁与统计？`)) return;
    setBusy(channel);
    try {
      await endpoints.channels.reset(channel);
      toast("已重置", "success");
      await Promise.all([loadBans(channel), load()]);
    } catch (e) {
      toast(e instanceof Error ? e.message : "重置失败", "error");
    } finally {
      setBusy(null);
    }
  };

  const remove = async (channel: string) => {
    if (!window.confirm(`删除渠道「${channel}」的档案？下次有请求会重新建档。`)) return;
    setBusy(channel);
    try {
      await endpoints.channels.remove(channel);
      toast("已删除", "success");
      if (expanded === channel) setExpanded(null);
      await load();
    } catch (e) {
      toast(e instanceof Error ? e.message : "删除失败", "error");
    } finally {
      setBusy(null);
    }
  };

  const totalBans = items.reduce((sum, it) => sum + it.bans, 0);

  return (
    <div>
      <PageHeader
        title="渠道封禁"
        description="渠道 = 请求的目标站点。某个 IP 在某个站点上连续失败或被返回 403/429 时，只在该站点被临时禁用，其它站点照常使用。到期自动解封。"
        actions={<Button onClick={() => void load()}>刷新</Button>}
      />

      <Card>
        <CardHeader className="flex flex-wrap items-center justify-between gap-3">
          <CardTitle>
            渠道 {items.length} 个 · 当前封禁 {totalBans} 条
          </CardTitle>
          <div className="flex flex-wrap items-center gap-2">
            <Input
              placeholder="搜索渠道"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="w-48"
            />
            <label className="flex items-center gap-1.5 text-sm text-muted-foreground">
              <input
                type="checkbox"
                checked={onlyBanned}
                onChange={(e) => setOnlyBanned(e.target.checked)}
              />
              只看有封禁
            </label>
          </div>
        </CardHeader>
        <CardContent>
          <div className="overflow-x-auto rounded-2xl border border-white/50 dark:border-white/10">
            <table className="min-w-full text-left text-sm">
              <thead className="bg-white/40 text-xs text-muted-foreground dark:bg-white/5">
                <tr>
                  <th className="px-3 py-2.5">渠道</th>
                  <th className="px-3 py-2.5">成功</th>
                  <th className="px-3 py-2.5">失败</th>
                  <th className="px-3 py-2.5">超时</th>
                  <th className="px-3 py-2.5">失败率</th>
                  <th className="px-3 py-2.5">封禁</th>
                  <th className="px-3 py-2.5">操作</th>
                </tr>
              </thead>
              <tbody>
                {loading ? (
                  <tr>
                    <td colSpan={7} className="px-3 py-10 text-center text-muted-foreground">
                      加载中...
                    </td>
                  </tr>
                ) : items.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="px-3 py-10 text-center text-muted-foreground">
                      还没有渠道记录。代理走过单跳/链式出口，或调用方上报过结果之后，这里会自动建档。
                    </td>
                  </tr>
                ) : (
                  items.flatMap((it) => {
                    const open = expanded === it.name;
                    const rows = [
                      <tr
                        key={it.name}
                        className="row-hover border-t border-white/40 dark:border-white/5"
                      >
                        <td className="px-3 py-2.5">
                          <button
                            type="button"
                            className="font-mono text-xs font-medium underline-offset-2 hover:underline"
                            onClick={() => void toggle(it.name)}
                          >
                            {it.name}
                          </button>
                        </td>
                        <td className="px-3 py-2.5">{it.ok}</td>
                        <td className="px-3 py-2.5">{it.fail}</td>
                        <td className="px-3 py-2.5">{it.timeout}</td>
                        <td className="px-3 py-2.5">
                          {it.ok + it.fail > 0 ? percent(it.fail_rate) : "—"}
                        </td>
                        <td className="px-3 py-2.5">
                          {it.bans > 0 ? (
                            <span className="rounded-full bg-amber-500/15 px-2 py-0.5 text-[11px] font-medium text-amber-700 dark:text-amber-300">
                              {it.bans}
                            </span>
                          ) : (
                            <span className="text-muted-foreground">0</span>
                          )}
                        </td>
                        <td className="px-3 py-2.5">
                          <div className="flex flex-wrap gap-1.5">
                            <Button size="sm" variant="ghost" onClick={() => void toggle(it.name)}>
                              {open ? "收起" : "明细"}
                            </Button>
                            <Button
                              size="sm"
                              variant="secondary"
                              disabled={busy === it.name}
                              onClick={() => void reset(it.name)}
                            >
                              重置
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              disabled={busy === it.name}
                              onClick={() => void remove(it.name)}
                            >
                              删除
                            </Button>
                          </div>
                        </td>
                      </tr>,
                    ];
                    if (open) {
                      const list = bans[it.name] || [];
                      rows.push(
                        <tr key={`${it.name}-detail`} className="border-t border-white/40 dark:border-white/5">
                          <td colSpan={7} className="bg-white/30 px-3 py-3 dark:bg-white/5">
                            {list.length === 0 ? (
                              <div className="text-xs text-muted-foreground">该渠道当前没有封禁。</div>
                            ) : (
                              <div className="space-y-1.5">
                                {list.map((b) => (
                                  <div
                                    key={b.addr}
                                    className="flex flex-wrap items-center gap-2 text-xs"
                                  >
                                    <span className="font-mono font-medium">{b.addr}</span>
                                    <span className="text-muted-foreground">
                                      {describeReason(b.reason)}
                                    </span>
                                    <span className="text-muted-foreground">
                                      {b.pending ? "待复检（看到成功才解封）" : `剩余 ${remainingText(b.until)}`}
                                    </span>
                                    <span className="text-muted-foreground">
                                      第 {b.strikes} 次 · 本次 {b.ttl_sec}s
                                    </span>
                                    <Button
                                      size="sm"
                                      variant="ghost"
                                      disabled={busy === `${it.name}|${b.addr}`}
                                      onClick={() => void unban(it.name, b.addr)}
                                    >
                                      解封
                                    </Button>
                                    <Button
                                      size="sm"
                                      variant="ghost"
                                      onClick={async () => {
                                        try {
                                          await endpoints.channels.allow({
                                            channel: it.name,
                                            addr: b.addr,
                                            reason: "panel",
                                          });
                                          toast(`${b.addr} 已加入白名单，不再自动禁`, "success");
                                          await Promise.all([loadBans(it.name), load()]);
                                        } catch (e) {
                                          toast(e instanceof Error ? e.message : "加白失败", "error");
                                        }
                                      }}
                                    >
                                      永不自动禁
                                    </Button>
                                  </div>
                                ))}
                              </div>
                            )}
                          </td>
                        </tr>,
                      );
                    }
                    return rows;
                  })
                )}
              </tbody>
            </table>
          </div>
          <p className="mt-3 text-xs text-muted-foreground">
            HTTPS 走 CONNECT 隧道，代理池只能看到「连上了没有」，看不到里面的 403 / 429 /
            验证码。这类应用层信号需要调用方回传：
            <code className="mx-1 rounded bg-white/50 px-1 py-0.5 font-mono dark:bg-white/10">
              POST /api/channels/report
            </code>
            。阈值与封禁时长在「系统设置 → 渠道策略」调整。
          </p>
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>自定义规则 · {rules.length} 条</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-xs text-muted-foreground">
            在全局默认（403/429、连续失败、失败率、超时）之外再加条件。渠道留空表示对所有站点生效。
          </p>
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            <Input
              placeholder="名称（可选）"
              value={ruleForm.name}
              onChange={(e) => setRuleForm({ ...ruleForm, name: e.target.value })}
            />
            <Input
              placeholder="渠道，空=全部。例 taobao.com"
              value={ruleForm.channel}
              onChange={(e) => setRuleForm({ ...ruleForm, channel: e.target.value })}
            />
            <Select value={ruleForm.kind} onChange={(e) => setRuleForm({ ...ruleForm, kind: e.target.value })}>
              <option value="status">状态码命中</option>
              <option value="consecutive">连续失败</option>
              <option value="fail_rate">失败率</option>
              <option value="timeouts">超时次数</option>
              <option value="error">错误标签包含</option>
            </Select>
            {ruleForm.kind === "status" ? (
              <Input
                placeholder="状态码，逗号分隔。例 503,401"
                value={ruleForm.statuses}
                onChange={(e) => setRuleForm({ ...ruleForm, statuses: e.target.value })}
              />
            ) : null}
            {ruleForm.kind === "consecutive" || ruleForm.kind === "timeouts" ? (
              <Input
                type="number"
                placeholder="阈值"
                value={ruleForm.threshold}
                onChange={(e) => setRuleForm({ ...ruleForm, threshold: e.target.value })}
              />
            ) : null}
            {ruleForm.kind === "fail_rate" ? (
              <Input
                type="number"
                step="0.05"
                min="0"
                max="1"
                placeholder="失败率 0–1"
                value={ruleForm.rate}
                onChange={(e) => setRuleForm({ ...ruleForm, rate: e.target.value })}
              />
            ) : null}
            {ruleForm.kind === "error" ? (
              <Input
                placeholder="匹配错误标签。例 captcha"
                value={ruleForm.match}
                onChange={(e) => setRuleForm({ ...ruleForm, match: e.target.value })}
              />
            ) : null}
            <Input
              type="number"
              placeholder="封禁秒数，空=用全局"
              value={ruleForm.ttl_sec}
              onChange={(e) => setRuleForm({ ...ruleForm, ttl_sec: e.target.value })}
            />
            <Button
              onClick={async () => {
                try {
                  const statuses = ruleForm.statuses
                    .split(",")
                    .map((s) => Number(s.trim()))
                    .filter((n) => Number.isFinite(n) && n > 0);
                  await endpoints.channels.addRule({
                    name: ruleForm.name,
                    channel: ruleForm.channel,
                    kind: ruleForm.kind,
                    statuses: ruleForm.kind === "status" ? statuses : undefined,
                    threshold:
                      ruleForm.kind === "consecutive" || ruleForm.kind === "timeouts"
                        ? Number(ruleForm.threshold) || undefined
                        : undefined,
                    rate: ruleForm.kind === "fail_rate" ? Number(ruleForm.rate) || undefined : undefined,
                    match: ruleForm.kind === "error" ? ruleForm.match : undefined,
                    ttl_sec: ruleForm.ttl_sec ? Number(ruleForm.ttl_sec) : undefined,
                    enabled: true,
                  });
                  toast("规则已添加", "success");
                  await load();
                } catch (e) {
                  toast(e instanceof Error ? e.message : "添加失败", "error");
                }
              }}
            >
              添加规则
            </Button>
          </div>
          {rules.length === 0 ? (
            <div className="text-sm text-muted-foreground">还没有自定义规则。</div>
          ) : (
            <div className="space-y-1.5 text-xs">
              {rules.map((rule) => (
                <div key={rule.id} className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{rule.name || rule.kind}</span>
                  <span className="text-muted-foreground">{rule.channel || "全部渠道"}</span>
                  <span className="text-muted-foreground">
                    {rule.kind === "status"
                      ? `状态码 ${(rule.statuses || []).join(",")}`
                      : rule.kind === "consecutive"
                        ? `连续失败 ≥ ${rule.threshold || 3}`
                        : rule.kind === "fail_rate"
                          ? `失败率 ≥ ${rule.rate || 0.6}`
                          : rule.kind === "timeouts"
                            ? `超时 ≥ ${rule.threshold || 5}`
                            : `错误包含 ${rule.match}`}
                    {rule.ttl_sec ? ` · ${rule.ttl_sec}s` : ""}
                  </span>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={async () => {
                      try {
                        await endpoints.channels.deleteRule(rule.id);
                        toast("已删除", "success");
                        await load();
                      } catch (e) {
                        toast(e instanceof Error ? e.message : "删除失败", "error");
                      }
                    }}
                  >
                    删除
                  </Button>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader className="flex flex-wrap items-center justify-between gap-3">
          <CardTitle>
            请求日志{expanded ? ` · ${expanded}` : ""} · {logs.length} 条
          </CardTitle>
          <Button
            variant="secondary"
            onClick={async () => {
              try {
                await endpoints.channels.clearLogs();
                setLogs([]);
              } catch (e) {
                toast(e instanceof Error ? e.message : "清空失败", "error");
              }
            }}
          >
            清空
          </Button>
        </CardHeader>
        <CardContent>
          <div
            ref={logRef}
            className="max-h-80 overflow-auto rounded-2xl border border-white/50 bg-white/30 p-3 font-mono text-[11px] leading-5 dark:border-white/10 dark:bg-black/20"
          >
            {logs.length === 0 ? (
              <div className="text-muted-foreground">还没有请求记录。走单跳/链式出口，或调用方上报之后会出现在这里。</div>
            ) : (
              logs.map((line, i) => (
                <div key={`${line.at}-${line.addr}-${i}`} className={line.banned ? "text-amber-700 dark:text-amber-300" : line.ok ? "" : "text-rose-700 dark:text-rose-300"}>
                  <span className="text-muted-foreground">{new Date(line.at).toLocaleTimeString()}</span>
                  {"  "}
                  <span className="font-medium">{line.channel}</span>
                  {"  "}
                  <span>{line.addr}</span>
                  {"  "}
                  {line.ok ? "ok" : "fail"}
                  {line.status ? ` ${line.status}` : ""}
                  {line.err ? ` ${line.err}` : ""}
                  {line.latency_ms ? ` ${line.latency_ms}ms` : ""}
                  {line.reported ? " 上报" : ""}
                  {line.banned ? ` → 封禁 ${describeReason(line.reason || "")}` : ""}
                </div>
              ))
            )}
          </div>
          <p className="mt-2 text-xs text-muted-foreground">
            最近请求记录，触发封禁的那一行会高亮。点开某个渠道时只看该渠道。重启后仍保留一段时间（默认 48 小时）。
          </p>
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>白名单 · {allows.length} 条</CardTitle>
        </CardHeader>
        <CardContent>
          {allows.length === 0 ? (
            <div className="text-sm text-muted-foreground">
              还没有保护名单。在封禁明细里点「永不自动禁」，或买来的住宅 IP 可以加到这里，自动规则不会再动它。
            </div>
          ) : (
            <div className="space-y-1.5 text-xs">
              {allows.map((a) => (
                <div key={`${a.channel}|${a.addr}`} className="flex flex-wrap items-center gap-2">
                  <span className="font-mono font-medium">{a.addr}</span>
                  <span className="text-muted-foreground">{a.channel || "全部渠道"}</span>
                  {a.reason ? <span className="text-muted-foreground">· {a.reason}</span> : null}
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={async () => {
                      try {
                        await endpoints.channels.deny(a.addr, a.channel);
                        toast("已移出白名单", "success");
                        await load();
                      } catch (e) {
                        toast(e instanceof Error ? e.message : "移除失败", "error");
                      }
                    }}
                  >
                    移除
                  </Button>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
