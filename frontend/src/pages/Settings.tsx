import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { endpoints } from "@/api";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { PageHeader } from "@/components/PageHeader";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input, Select } from "@/components/ui/input";
import { useToast } from "@/hooks/useToast";
import type { ChainOptions, ChannelPolicyConfig, DirectProxyStatus, MihomoStatus, Settings } from "@/types";

function chainPathPreview(hops: number) {
  const n = Math.max(2, Math.min(4, hops || 2));
  const parts = ["本机", "入口"];
  for (let i = 0; i < n - 2; i++) parts.push("中继");
  parts.push("出口", "目标");
  return parts.join(" → ");
}

function defaultChain(partial?: ChainOptions): ChainOptions {
  return {
    enabled: true,
    listen_addr: "0.0.0.0:7893",
    hops: 2,
    failover_tries: 6,
    dial_timeout_ms: 8000,
    hop_timeout_ms: 5000,
    prefer_distinct_host: true,
    prefer_distinct_region: false,
    sticky_enabled: false,
    sticky_ttl_sec: 600,
    auth_required: false,
    max_parallel_dial: 1,
    rate_limit_bps: 0,
    entry_proto: "",
    exit_proto: "",
    entry_region: "",
    exit_region: "",
    allowed_cidrs: [],
    ...partial,
  };
}

export function SettingsPage() {
  const { toast } = useToast();
  const [settings, setSettings] = useState<Settings | null>(null);
  const [mihomo, setMihomo] = useState<MihomoStatus | null>(null);
  const [direct, setDirect] = useState<DirectProxyStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [passwordForm, setPasswordForm] = useState({ old_password: "", new_password: "" });
  const [restartOpen, setRestartOpen] = useState(false);
  const [restarting, setRestarting] = useState(false);
  const [ver, setVer] = useState<{ commit?: string; short?: string; time?: string } | null>(null);
  const [upd, setUpd] = useState<{
    local_short?: string;
    remote_short?: string;
    newer?: boolean;
    local_commit?: string;
    remote_commit?: string;
  } | null>(null);
  const [updBusy, setUpdBusy] = useState(false);
  const [updOpen, setUpdOpen] = useState(false);

  const load = useCallback(async () => {
    try {
      const [s, status, dp] = await Promise.all([
        endpoints.settings.get(),
        endpoints.mihomo.status().catch(() => null),
        endpoints.directProxy.status().catch(() => null),
      ]);
      setSettings(s);
      setMihomo(status);
      setDirect(dp);
      endpoints.system.version().then(setVer).catch(() => setVer(null));
    } catch (error) {
      toast(error instanceof Error ? error.message : "加载失败", "error");
    } finally {
      setLoading(false);
    }
  }, [toast]);

  useEffect(() => {
    void load();
  }, [load]);

  const chain = useMemo(() => {
    const fromFeat = (settings?.feature as { chain?: ChainOptions } | undefined)?.chain;
    const fromStatus = direct?.chain_options;
    return defaultChain({
      ...fromFeat,
      ...fromStatus,
      hops: settings?.proxy_chain_hops ?? fromStatus?.hops ?? fromFeat?.hops ?? 2,
      enabled: fromStatus?.enabled ?? fromFeat?.enabled ?? direct?.chain_enabled ?? true,
      listen_addr: fromStatus?.listen_addr || fromFeat?.listen_addr || direct?.chain_listen_addr || "0.0.0.0:7893",
    });
  }, [settings, direct]);

  const hops = chain.hops ?? 2;
  const pathPreview = useMemo(() => chainPathPreview(hops), [hops]);

  const patchChain = (patch: Partial<ChainOptions>) => {
    if (!settings) return;
    const nextChain = { ...chain, ...patch };
    const feat = { ...(settings.feature || {}), chain: nextChain };
    setSettings({
      ...settings,
      proxy_chain_hops: nextChain.hops ?? 2,
      feature: feat,
      feature_json: JSON.stringify(feat),
    });
  };

  // 渠道策略只存在 feature_json 里，没有对应的顶层 settings 字段，
  // 所以补丁比 chain 简单：只合并这一个子对象。
  const channels = useMemo(
    () => ((settings?.feature as { channels?: ChannelPolicyConfig } | undefined)?.channels ?? {}),
    [settings],
  );

  const patchChannels = (patch: Partial<ChannelPolicyConfig>) => {
    if (!settings) return;
    const next = { ...(settings.feature || {}), channels: { ...channels, ...patch } };
    setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
  };

  const applyChainNow = async () => {
    try {
      const st = await endpoints.directProxy.updateChain(chain);
      setDirect(st);
      toast("链式代理配置已应用", "success");
    } catch (err) {
      toast(err instanceof Error ? err.message : "应用失败", "error");
    }
  };

  const saveSettings = async (event: FormEvent) => {
    event.preventDefault();
    if (!settings) return;
    try {
      const result = await endpoints.settings.update(settings);
      if (result && typeof result === "object" && "settings" in result && result.settings) {
        setSettings(result.settings);
        toast(result.apply_message || "设置已保存", "success");
      } else {
        setSettings((result as unknown as Settings) || settings);
        toast("设置已保存", "success");
      }
      const dp = await endpoints.directProxy.status().catch(() => null);
      setDirect(dp);
    } catch (error) {
      toast(error instanceof Error ? error.message : "保存失败", "error");
    }
  };

  const changePassword = async (event: FormEvent) => {
    event.preventDefault();
    try {
      await endpoints.auth.changePassword(passwordForm.old_password, passwordForm.new_password);
      toast("密码已修改，请重新登录", "success");
      setPasswordForm({ old_password: "", new_password: "" });
      window.location.href = "/login";
    } catch (error) {
      toast(error instanceof Error ? error.message : "修改失败", "error");
    }
  };

  const doRestart = async () => {
    setRestarting(true);
    try {
      await endpoints.system.restart();
      toast("重启请求已发送", "success");
      setRestartOpen(false);
    } catch (error) {
      toast(error instanceof Error ? error.message : "重启失败", "error");
    } finally {
      setRestarting(false);
    }
  };

  if (loading || !settings) {
    return <div className="text-sm text-muted-foreground">加载中...</div>;
  }

  return (
    <div>
      <PageHeader title="系统设置" description="面板、会话、免费代理调度、链式出口" />
      <form className="grid gap-4 xl:grid-cols-2" onSubmit={saveSettings}>
        <div className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>面板与会话</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="面板 Host">
                  <Input value={settings.panel_host} onChange={(e) => setSettings({ ...settings, panel_host: e.target.value })} />
                </Field>
                <Field label="面板端口">
                  <Input type="number" value={settings.panel_port} onChange={(e) => setSettings({ ...settings, panel_port: Number(e.target.value) })} />
                </Field>
                <Field label="会话有效期（秒）">
                  <Input
                    type="number"
                    value={settings.session_max_age_sec ?? 604800}
                    onChange={(e) => setSettings({ ...settings, session_max_age_sec: Number(e.target.value) })}
                  />
                </Field>
                <Field label="日志级别">
                  <Select value={settings.log_level} onChange={(e) => setSettings({ ...settings, log_level: e.target.value })}>
                    {["debug", "info", "warning", "error"].map((level) => (
                      <option key={level} value={level}>{level}</option>
                    ))}
                  </Select>
                </Field>
                <Field label="默认订阅间隔（秒）">
                  <Input
                    type="number"
                    value={settings.default_subscription_interval_sec}
                    onChange={(e) => setSettings({ ...settings, default_subscription_interval_sec: Number(e.target.value) })}
                  />
                </Field>
                <Field label="Mihomo Controller Secret">
                  <Input value={settings.mihomo_controller_secret} onChange={(e) => setSettings({ ...settings, mihomo_controller_secret: e.target.value })} />
                </Field>
              </div>
              <p className="text-xs text-muted-foreground">会话默认 604800 秒（7 天）。修改面板 Host/端口后需重启。</p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>节点探测</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="延迟测试 URL">
                  <Input value={settings.latency_test_url} onChange={(e) => setSettings({ ...settings, latency_test_url: e.target.value })} />
                </Field>
                <Field label="测速 URL">
                  <Input value={settings.speed_test_url} onChange={(e) => setSettings({ ...settings, speed_test_url: e.target.value })} />
                </Field>
                <Field label="延迟超时 ms">
                  <Input type="number" value={settings.latency_timeout_ms} onChange={(e) => setSettings({ ...settings, latency_timeout_ms: Number(e.target.value) })} />
                </Field>
                <Field label="测速超时 ms">
                  <Input type="number" value={settings.speed_timeout_ms} onChange={(e) => setSettings({ ...settings, speed_timeout_ms: Number(e.target.value) })} />
                </Field>
                <Field label="延迟并发">
                  <Input type="number" value={settings.latency_concurrency} onChange={(e) => setSettings({ ...settings, latency_concurrency: Number(e.target.value) })} />
                </Field>
                <Field label="测速并发">
                  <Input type="number" value={settings.speed_concurrency} onChange={(e) => setSettings({ ...settings, speed_concurrency: Number(e.target.value) })} />
                </Field>
                <Field label="失败重试">
                  <Input type="number" value={settings.failure_retry_count} onChange={(e) => setSettings({ ...settings, failure_retry_count: Number(e.target.value) })} />
                </Field>
                <Field label="测速最大字节">
                  <Input type="number" value={settings.speed_max_bytes} onChange={(e) => setSettings({ ...settings, speed_max_bytes: Number(e.target.value) })} />
                </Field>
              </div>
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={settings.speed_test_enabled}
                  onChange={(e) => setSettings({ ...settings, speed_test_enabled: e.target.checked })}
                />
                启用测速
              </label>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>免费代理调度</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="采集周期（秒）">
                  <Input
                    type="number"
                    value={settings.scrape_interval_sec ?? 300}
                    onChange={(e) => setSettings({ ...settings, scrape_interval_sec: Number(e.target.value) })}
                  />
                </Field>
                <Field label="校验周期（秒）">
                  <Input
                    type="number"
                    value={settings.validate_interval_sec ?? 120}
                    onChange={(e) => setSettings({ ...settings, validate_interval_sec: Number(e.target.value) })}
                  />
                </Field>
                <Field label="校验 URL">
                  <Input
                    value={settings.free_validate_url ?? ""}
                    onChange={(e) => setSettings({ ...settings, free_validate_url: e.target.value })}
                  />
                </Field>
                <Field label="校验超时 ms">
                  <Input
                    type="number"
                    value={settings.free_validate_timeout_ms ?? 8000}
                    onChange={(e) => setSettings({ ...settings, free_validate_timeout_ms: Number(e.target.value) })}
                  />
                </Field>
                <Field label="校验并发">
                  <Input
                    type="number"
                    value={settings.free_validate_concurrency ?? 32}
                    onChange={(e) => setSettings({ ...settings, free_validate_concurrency: Number(e.target.value) })}
                  />
                </Field>
                <Field label="采集出网代理">
                  <Input
                    value={typeof settings.feature?.scrape_proxy === "string" ? settings.feature.scrape_proxy : ""}
                    onChange={(e) => {
                      const next = { ...(settings.feature || {}), scrape_proxy: e.target.value };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                    placeholder="空=直连，失败再走 mihomo 池；none=只直连；chain / socks5://…"
                  />
                </Field>
              </div>
              <p className="text-xs text-muted-foreground">
                采集≥60s，校验≥30s；保存后调度器下一轮生效。空着时先直连（国内 jsdmirror），GitHub/ghproxy TLS 失败再走已发布出口池。填 none 则只直连。
              </p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>仪表盘卡片</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="grid gap-2 sm:grid-cols-2">
                {[
                  ["available", "实时可用代理"],
                  ["health", "节点健康率"],
                  ["live_conn", "实时连接数"],
                  ["up_bytes", "上行流量"],
                  ["down_bytes", "下行流量"],
                  ["sources", "采集源"],
                  ["avg_score", "平均评分"],
                  ["total", "代理总数"],
                  ["single_hop", "单跳出口"],
                  ["chain", "链式代理"],
                  ["lan", "局域网访问"],
                  ["events", "最近动态"],
                  ["regions", "地区分布"],
                  ["channel_bans", "渠道封禁"],
                ].map(([key, label]) => {
                  const cards = (settings.feature?.dashboard_cards || {}) as Record<string, boolean>;
                  const on = cards[key] !== false;
                  return (
                    <label key={key} className="flex items-center gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={on}
                        onChange={(e) => {
                          const next = {
                            ...(settings.feature || {}),
                            dashboard_cards: { ...cards, [key]: e.target.checked },
                          };
                          setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                        }}
                      />
                      {label}
                    </label>
                  );
                })}
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>国家过滤</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <p className="text-xs text-muted-foreground">
                默认永久去掉中国大陆节点（ISO CN）。采集源声明、主机 GeoIP、经代理探测到的真实出口，任一命中即丢弃。香港/台湾/澳门默认保留。保存后立即清除池里已有的匹配项。
              </p>
              <div className="grid gap-3 sm:grid-cols-2">
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={settings.feature?.country_filter_enabled !== false}
                    onChange={(e) => {
                      const next = { ...(settings.feature || {}), country_filter_enabled: e.target.checked };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                  />
                  启用国家屏蔽
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={settings.feature?.check_exit_country !== false}
                    onChange={(e) => {
                      const next = { ...(settings.feature || {}), check_exit_country: e.target.checked };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                  />
                  检查真实出口国家（经代理访问 ip-api）
                </label>
                <Field label="屏蔽国家代码（逗号分隔，默认 CN）">
                  <Input
                    value={Array.isArray(settings.feature?.blocked_countries)
                      ? (settings.feature?.blocked_countries as string[]).join(",")
                      : "CN"}
                    onChange={(e) => {
                      const codes = e.target.value.split(",").map((s) => s.trim()).filter(Boolean);
                      const next = { ...(settings.feature || {}), blocked_countries: codes };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                    placeholder="CN"
                  />
                </Field>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>高级功能（F3–F6）</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <div className="grid gap-3 sm:grid-cols-2">
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={Boolean(settings.feature?.direct_sticky_enabled)}
                    onChange={(e) => {
                      const next = { ...(settings.feature || {}), direct_sticky_enabled: e.target.checked };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                  />
                  出口粘性会话
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={Boolean(settings.feature?.direct_auth_required)}
                    onChange={(e) => {
                      const next = { ...(settings.feature || {}), direct_auth_required: e.target.checked };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                  />
                  强制 DirectProxy 认证
                </label>
                <Field label="粘性 TTL 秒">
                  <Input
                    type="number"
                    value={Number(settings.feature?.sticky_ttl_sec ?? 600)}
                    onChange={(e) => {
                      const next = { ...(settings.feature || {}), sticky_ttl_sec: Number(e.target.value) };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                  />
                </Field>
                <Field label="限速 B/s（0=不限）">
                  <Input
                    type="number"
                    value={Number(settings.feature?.rate_limit_bytes_per_sec ?? 0)}
                    onChange={(e) => {
                      const next = { ...(settings.feature || {}), rate_limit_bytes_per_sec: Number(e.target.value) };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                  />
                </Field>
                <Field label="Webhook URL">
                  <Input
                    value={String(settings.feature?.webhook_url || "")}
                    onChange={(e) => {
                      const next = { ...(settings.feature || {}), webhook_url: e.target.value };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                  />
                </Field>
                <Field label="Webhook 触发事件（逗号分隔，* = 全部）">
                  <Input
                    value={Array.isArray(settings.feature?.webhook_events)
                      ? (settings.feature?.webhook_events as string[]).join(",")
                      : "validated_low,validate_all_fail"}
                    onChange={(e) => {
                      const events = e.target.value.split(",").map((s) => s.trim()).filter(Boolean);
                      const next = { ...(settings.feature || {}), webhook_events: events };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                    placeholder="validated_low,validate_all_fail,channel_ban,*"
                  />
                </Field>
                <Field label="来源自动禁用率（0=关，0.1–1.0）">
                  <Input
                    type="number"
                    step="0.01"
                    min="0"
                    max="1"
                    value={Number(settings.feature?.source_auto_disable_rate ?? 0.15)}
                    onChange={(e) => {
                      const next = { ...(settings.feature || {}), source_auto_disable_rate: Number(e.target.value) };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                  />
                </Field>
                <Field label="来源禁用最低样本数">
                  <Input
                    type="number"
                    value={Number(settings.feature?.source_min_samples ?? 20)}
                    onChange={(e) => {
                      const next = { ...(settings.feature || {}), source_min_samples: Number(e.target.value) };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                  />
                </Field>
                <Field label="告警：可用代理下限">
                  <Input
                    type="number"
                    value={Number(settings.feature?.alert_validated_min ?? 5)}
                    onChange={(e) => {
                      const next = { ...(settings.feature || {}), alert_validated_min: Number(e.target.value) };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                  />
                </Field>
                <Field label="允许 CIDR（逗号分隔）">
                  <Input
                    value={Array.isArray(settings.feature?.allowed_cidrs) ? (settings.feature?.allowed_cidrs as string[]).join(",") : ""}
                    onChange={(e) => {
                      const cidrs = e.target.value.split(",").map((s) => s.trim()).filter(Boolean);
                      const next = { ...(settings.feature || {}), allowed_cidrs: cidrs };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                    placeholder="空=仅局域网；可加 100.64.0.0/10"
                  />
                </Field>
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={Boolean(settings.feature?.public_open)}
                    onChange={(e) => {
                      const next = { ...(settings.feature || {}), public_open: e.target.checked };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                  />
                  公网开放 /api/public（危险：取代理/入池不再限局域网）
                </label>
                <Field label="多校验 URL（换行）">
                  <Input
                    value={Array.isArray(settings.feature?.free_validate_urls) ? (settings.feature?.free_validate_urls as string[]).join("\n") : ""}
                    onChange={(e) => {
                      const urls = e.target.value.split(/[\n,]+/).map((s) => s.trim()).filter(Boolean);
                      const next = { ...(settings.feature || {}), free_validate_urls: urls };
                      setSettings({ ...settings, feature: next, feature_json: JSON.stringify(next) });
                    }}
                  />
                </Field>
              </div>
              <p className="text-xs text-muted-foreground">保存后热更新校验 URL / Webhook / CIDR / 限速等。API Token、审计见下方系统卡片。</p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>渠道策略与选路</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <p className="text-xs text-muted-foreground">
                渠道 = 请求的目标站点。任一规则命中即把该 IP 在该渠道上临时禁用，其它渠道不受影响。
                阈值填 0 表示关闭这条规则。明细见「渠道封禁」页。
              </p>
              <div className="grid gap-3 sm:grid-cols-2">
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={channels.enabled !== false}
                    onChange={(e) => patchChannels({ enabled: e.target.checked })}
                  />
                  启用渠道封禁
                </label>
                <Field label="渠道粒度">
                  <Select
                    value={String(channels.key_mode || "etld1")}
                    onChange={(e) => patchChannels({ key_mode: e.target.value })}
                  >
                    <option value="etld1">按注册域（item./www. 合并）</option>
                    <option value="host">按完整域名（子域各自独立）</option>
                    <option value="off">不分渠道（等于全局）</option>
                  </Select>
                </Field>
                <Field label="选路策略">
                  <Select
                    value={String(channels.pick_strategy || "weighted")}
                    onChange={(e) => patchChannels({ pick_strategy: e.target.value })}
                  >
                    <option value="weighted">按质量加权随机</option>
                    <option value="p2c">P2C 二选一（更稳、更省）</option>
                    <option value="random">等概率随机</option>
                    <option value="rr">按渠道轮转</option>
                  </Select>
                </Field>
                <Field label="重复取用冷却秒（0=关）">
                  <Input
                    type="number"
                    min="0"
                    value={Number(channels.cooldown_sec ?? 30)}
                    onChange={(e) => patchChannels({ cooldown_sec: Number(e.target.value) })}
                  />
                </Field>
                <Field label="统计窗口秒">
                  <Input
                    type="number"
                    min="1"
                    value={Number(channels.window_sec ?? 300)}
                    onChange={(e) => patchChannels({ window_sec: Number(e.target.value) })}
                  />
                </Field>
                <Field label="连续失败次数（0=关）">
                  <Input
                    type="number"
                    min="0"
                    value={Number(channels.consecutive_fails ?? 3)}
                    onChange={(e) => patchChannels({ consecutive_fails: Number(e.target.value) })}
                  />
                </Field>
                <Field label="失败率阈值（0=关，0–1）">
                  <Input
                    type="number"
                    step="0.05"
                    min="0"
                    max="1"
                    value={Number(channels.fail_rate ?? 0.6)}
                    onChange={(e) => patchChannels({ fail_rate: Number(e.target.value) })}
                  />
                </Field>
                <Field label="失败率最低样本数">
                  <Input
                    type="number"
                    min="1"
                    value={Number(channels.min_samples ?? 5)}
                    onChange={(e) => patchChannels({ min_samples: Number(e.target.value) })}
                  />
                </Field>
                <Field label="超时次数（0=关）">
                  <Input
                    type="number"
                    min="0"
                    value={Number(channels.timeout_fails ?? 5)}
                    onChange={(e) => patchChannels({ timeout_fails: Number(e.target.value) })}
                  />
                </Field>
                <Field label="即时封禁状态码（逗号分隔）">
                  <Input
                    value={(channels.ban_statuses ?? [403, 429]).join(",")}
                    onChange={(e) => {
                      const codes = e.target.value
                        .split(",")
                        .map((s) => Number(s.trim()))
                        .filter((n) => Number.isFinite(n) && n > 0);
                      patchChannels({ ban_statuses: codes });
                    }}
                    placeholder="403,429"
                  />
                </Field>
                <Field label="首次封禁秒">
                  <Input
                    type="number"
                    min="1"
                    value={Number(channels.ban_ttl_sec ?? 60)}
                    onChange={(e) => patchChannels({ ban_ttl_sec: Number(e.target.value) })}
                  />
                </Field>
                <Field label="封禁上限秒（重复翻倍到此为止）">
                  <Input
                    type="number"
                    min="1"
                    value={Number(channels.ban_ttl_max_sec ?? 1800)}
                    onChange={(e) => patchChannels({ ban_ttl_max_sec: Number(e.target.value) })}
                  />
                </Field>
                <Field label="最多跟踪渠道数">
                  <Input
                    type="number"
                    min="1"
                    value={Number(channels.max_channels ?? 500)}
                    onChange={(e) => patchChannels({ max_channels: Number(e.target.value) })}
                  />
                </Field>
                <Field label="每渠道最多跟踪 IP 数">
                  <Input
                    type="number"
                    min="1"
                    value={Number(channels.max_entries_per_chan ?? 2000)}
                    onChange={(e) => patchChannels({ max_entries_per_chan: Number(e.target.value) })}
                  />
                </Field>
                <Field label="请求日志保留小时">
                  <Input
                    type="number"
                    min="1"
                    value={Number(channels.log_retain_hours ?? 48)}
                    onChange={(e) => patchChannels({ log_retain_hours: Number(e.target.value) })}
                  />
                </Field>
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={channels.reprobe_on_expiry !== false}
                    onChange={(e) => patchChannels({ reprobe_on_expiry: e.target.checked })}
                  />
                  到期先复检再放回（看到成功才真正解封）
                </label>
              </div>
              <p className="text-xs text-muted-foreground">
                HTTPS 走 CONNECT 隧道，池子只能看到能否连通，看不到里面的状态码；这类信号需调用方通过
                <code className="mx-1 rounded bg-white/50 px-1 py-0.5 font-mono dark:bg-white/10">
                  POST /api/channels/report
                </code>
                回传。保存后即时生效。
              </p>
            </CardContent>
          </Card>

          <Button type="submit">保存全部设置</Button>
        </div>

        <div className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>出口：单跳 DirectProxy</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2 text-sm text-muted-foreground">
              <div>启用：{String(direct?.enabled ?? "-")} · 运行中：{String(direct?.running ?? "-")}</div>
              <div>监听：{String(direct?.listen_addr ?? "-")}</div>
              <div className="break-all font-mono text-xs text-foreground">HTTP：{String(direct?.client_http ?? "-")}</div>
              <div className="break-all font-mono text-xs text-foreground">SOCKS5：{String(direct?.client_socks5 ?? "-")}</div>
              <div>请求 / 成功 / 失败：{String(direct?.requests ?? 0)} / {String(direct?.success ?? 0)} / {String(direct?.failures ?? 0)}</div>
              <div className="text-xs">路径：本机 → 单跳代理 → 目标</div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>链式代理</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3 text-sm text-muted-foreground">
              <div className="rounded-2xl bg-white/50 p-3 dark:bg-white/5">
                <div className="text-xs text-muted-foreground">流量路径预览</div>
                <div className="mt-1 font-medium text-foreground">{pathPreview}</div>
              </div>
              <div>
                运行中：{String(direct?.chain_running ?? "-")} · 成功/失败{" "}
                {String(direct?.chain_success ?? 0)}/{String(direct?.chain_failures ?? 0)}
              </div>
              <div className="break-all font-mono text-xs text-foreground">HTTP：{String(direct?.chain_http ?? "-")}</div>
              <div className="break-all font-mono text-xs text-foreground">SOCKS5：{String(direct?.chain_socks5 ?? "-")}</div>

              <label className="flex items-center gap-2 text-sm text-foreground">
                <input type="checkbox" checked={Boolean(chain.enabled)} onChange={(e) => patchChain({ enabled: e.target.checked })} />
                启用链式监听
              </label>

              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="监听地址">
                  <Input value={chain.listen_addr || ""} onChange={(e) => patchChain({ listen_addr: e.target.value })} />
                </Field>
                <Field label="跳数">
                  <Select value={String(chain.hops ?? 2)} onChange={(e) => patchChain({ hops: Number(e.target.value) })}>
                    <option value={2}>2 跳（入口→出口）</option>
                    <option value={3}>3 跳（入口→中继→出口）</option>
                    <option value={4}>4 跳</option>
                  </Select>
                </Field>
                <Field label="容错次数">
                  <Input type="number" value={chain.failover_tries ?? 6} onChange={(e) => patchChain({ failover_tries: Number(e.target.value) })} />
                </Field>
                <Field label="并行拨号 1–3">
                  <Input type="number" value={chain.max_parallel_dial ?? 1} onChange={(e) => patchChain({ max_parallel_dial: Number(e.target.value) })} />
                </Field>
                <Field label="总超时 ms">
                  <Input type="number" value={chain.dial_timeout_ms ?? 8000} onChange={(e) => patchChain({ dial_timeout_ms: Number(e.target.value) })} />
                </Field>
                <Field label="单跳超时 ms">
                  <Input type="number" value={chain.hop_timeout_ms ?? 5000} onChange={(e) => patchChain({ hop_timeout_ms: Number(e.target.value) })} />
                </Field>
                <Field label="固定 VPS（exit_via）">
                  <Input
                    value={chain.exit_via || ""}
                    onChange={(e) => patchChain({ exit_via: e.target.value })}
                    placeholder="socks5://user:pass@vps:1080"
                  />
                </Field>
                <Field label="VPS 位置">
                  <Select value={chain.exit_via_mode || "entry"} onChange={(e) => patchChain({ exit_via_mode: e.target.value })}>
                    <option value="entry">第一跳：本机→VPS→代理→网站（出口 IP = 代理）</option>
                    <option value="exit">最后一跳：本机→代理→VPS→网站（出口 IP = VPS）</option>
                  </Select>
                </Field>
                <Field label="入口协议">
                  <Select value={chain.entry_proto || ""} onChange={(e) => patchChain({ entry_proto: e.target.value })}>
                    <option value="">不限</option>
                    <option value="http">http</option>
                    <option value="socks5">socks5</option>
                  </Select>
                </Field>
                <Field label="出口协议">
                  <Select value={chain.exit_proto || ""} onChange={(e) => patchChain({ exit_proto: e.target.value })}>
                    <option value="">不限</option>
                    <option value="http">http</option>
                    <option value="socks5">socks5</option>
                  </Select>
                </Field>
                <Field label="入口地区">
                  <Input value={chain.entry_region || ""} onChange={(e) => patchChain({ entry_region: e.target.value })} placeholder="如 US / CN" />
                </Field>
                <Field label="出口地区">
                  <Input value={chain.exit_region || ""} onChange={(e) => patchChain({ exit_region: e.target.value })} placeholder="如 US / JP" />
                </Field>
                <Field label="链式限速 B/s">
                  <Input type="number" value={chain.rate_limit_bps ?? 0} onChange={(e) => patchChain({ rate_limit_bps: Number(e.target.value) })} />
                </Field>
                <Field label="粘性 TTL 秒">
                  <Input type="number" value={chain.sticky_ttl_sec ?? 600} onChange={(e) => patchChain({ sticky_ttl_sec: Number(e.target.value) })} />
                </Field>
              </div>

              <div className="flex flex-wrap gap-4 text-foreground">
                <label className="flex items-center gap-2 text-sm">
                  <input type="checkbox" checked={Boolean(chain.prefer_distinct_host)} onChange={(e) => patchChain({ prefer_distinct_host: e.target.checked })} />
                  去重 Host
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <input type="checkbox" checked={Boolean(chain.prefer_distinct_region)} onChange={(e) => patchChain({ prefer_distinct_region: e.target.checked })} />
                  去重地区
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <input type="checkbox" checked={Boolean(chain.sticky_enabled)} onChange={(e) => patchChain({ sticky_enabled: e.target.checked })} />
                  链式粘性
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <input type="checkbox" checked={Boolean(chain.auth_required)} onChange={(e) => patchChain({ auth_required: e.target.checked })} />
                  强制认证
                </label>
              </div>

              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="链式用户名">
                  <Input value={chain.username || ""} onChange={(e) => patchChain({ username: e.target.value })} />
                </Field>
                <Field label="链式密码">
                  <Input type="password" value={chain.password || ""} onChange={(e) => patchChain({ password: e.target.value })} />
                </Field>
                <Field label="链式 CIDR（逗号分隔）">
                  <Input
                    value={(chain.allowed_cidrs || []).join(",")}
                    onChange={(e) =>
                      patchChain({
                        allowed_cidrs: e.target.value.split(",").map((s) => s.trim()).filter(Boolean),
                      })
                    }
                  />
                </Field>
              </div>

              <div className="flex flex-wrap gap-2">
                <Button type="button" onClick={() => void applyChainNow()}>
                  立即应用链式配置
                </Button>
                <Button type="button" variant="secondary" onClick={() => window.open("/api/direct-proxy/client-pack?os=linux&mode=chain", "_blank")}>
                  下载链式脚本
                </Button>
              </div>
              <p className="text-xs">也可点「保存全部设置」持久化。改监听地址可能需重启服务。</p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>热更新</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3 text-sm">
              <div>
                当前版本：<span className="font-mono">{ver?.short || ver?.commit || "dev"}</span>
                {ver?.time ? <span className="ml-2 text-muted-foreground">{ver.time}</span> : null}
              </div>
              {upd ? (
                <div>
                  远程 nightly：<span className="font-mono">{upd.remote_short || "-"}</span>
                  {upd.newer ? (
                    <span className="ml-2 text-amber-600">有新版本</span>
                  ) : (
                    <span className="ml-2 text-muted-foreground">已是最新</span>
                  )}
                </div>
              ) : null}
              <div className="flex flex-wrap gap-2">
                <Button
                  type="button"
                  variant="secondary"
                  onClick={async () => {
                    setUpdBusy(true);
                    try {
                      const st = await endpoints.system.updateCheck();
                      setUpd(st);
                      toast(st.newer ? `发现 ${st.remote_short}` : "已是最新", st.newer ? "success" : "success");
                    } catch (e) {
                      toast(e instanceof Error ? e.message : "检查失败", "error");
                    } finally {
                      setUpdBusy(false);
                    }
                  }}
                >
                  {updBusy ? "检查中…" : "检查更新"}
                </Button>
                <Button type="button" onClick={() => setUpdOpen(true)} disabled={updBusy}>
                  立即热更新
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">
                从 GitHub nightly 下载 linux/amd64 二进制并替换当前进程，无需在软路由上 docker build。下载期间面板会短暂中断，刷新即可。
              </p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>运行时只读</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2 text-sm text-muted-foreground">
              <div>Mihomo 运行中：{String(mihomo?.running ?? "-")}</div>
              <div>版本：{String(mihomo?.version ?? "-")}</div>
              <div className="break-all">路径：{String(mihomo?.binary_path ?? "-")}</div>
              <div className="text-xs">Redis / DATA_DIR 由环境变量配置，面板不可改。</div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>修改密码</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="space-y-3" onSubmit={changePassword}>
                <Field label="旧密码">
                  <Input
                    type="password"
                    value={passwordForm.old_password}
                    onChange={(e) => setPasswordForm({ ...passwordForm, old_password: e.target.value })}
                    required
                  />
                </Field>
                <Field label="新密码">
                  <Input
                    type="password"
                    value={passwordForm.new_password}
                    onChange={(e) => setPasswordForm({ ...passwordForm, new_password: e.target.value })}
                    required
                  />
                </Field>
                <Button type="button" onClick={(e) => void changePassword(e as unknown as FormEvent)}>修改密码</Button>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>API Token</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2 text-sm">
              <Button
                type="button"
                variant="secondary"
                onClick={async () => {
                  try {
                    const t = await endpoints.tokens.create("panel");
                    if (t.token) {
                      await navigator.clipboard?.writeText(t.token);
                      toast(`已创建并复制 Token：${t.token.slice(0, 16)}…`, "success");
                    } else toast("已创建", "success");
                  } catch (e) {
                    toast(e instanceof Error ? e.message : "创建失败", "error");
                  }
                }}
              >
                生成 Token
              </Button>
              <p className="text-xs text-muted-foreground">Bearer 头：Authorization: Bearer upp_xxx</p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>客户端脚本</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-wrap gap-2">
              <Button type="button" variant="secondary" onClick={() => window.open("/api/direct-proxy/client-pack?os=linux&mode=single", "_blank")}>
                单跳 Linux
              </Button>
              <Button type="button" variant="secondary" onClick={() => window.open("/api/direct-proxy/client-pack?os=linux&mode=chain", "_blank")}>
                链式 Linux
              </Button>
              <Button type="button" variant="secondary" onClick={() => window.open("/api/direct-proxy/client-pack?os=windows&mode=single", "_blank")}>
                Windows bat
              </Button>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>危险操作</CardTitle>
            </CardHeader>
            <CardContent>
              <Button type="button" variant="danger" onClick={() => setRestartOpen(true)}>重启服务</Button>
            </CardContent>
          </Card>
        </div>
      </form>

      <ConfirmDialog
        open={updOpen}
        title="热更新"
        description="将下载 GitHub nightly 二进制并替换当前进程。进行中的连接会中断，约几十秒后刷新面板即可。"
        confirmText="下载并切换"
        cancelText="取消"
        loading={updBusy}
        onCancel={() => {
          if (!updBusy) setUpdOpen(false);
        }}
        onConfirm={async () => {
          setUpdBusy(true);
          try {
            await endpoints.system.updateApply();
            toast("已切换，正在等待新进程…", "success");
            setUpdOpen(false);
            window.setTimeout(() => window.location.reload(), 4000);
          } catch (e) {
            toast(e instanceof Error ? e.message : "热更新失败", "error");
          } finally {
            setUpdBusy(false);
          }
        }}
      />

      <ConfirmDialog
        open={restartOpen}
        title="重启服务"
        description="确认重启 unified-proxy-pool？进行中的连接会被中断。"
        confirmText="重启"
        cancelText="取消"
        danger
        loading={restarting}
        onCancel={() => {
          if (!restarting) setRestartOpen(false);
        }}
        onConfirm={() => void doRestart()}
      />
    </div>
  );
}
