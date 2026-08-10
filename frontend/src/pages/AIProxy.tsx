import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, CheckCircle2, Edit3, RefreshCw, Search, Send, Trash2 } from "lucide-react";
import { endpoints } from "@/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useToast } from "@/hooks/useToast";
import { PageHeader } from "@/components/PageHeader";

type SubmitResult = {
  submitted?: number;
  added?: number;
  duplicates?: number;
  rejected?: number;
  net_growth?: number;
  evicted?: number;
  raw_at_cap?: boolean;
  source?: string;
  note?: string;
};

type InputPreview = {
  format: "JSON" | "文本";
  count: number;
  bytes: number;
  issue?: string;
};

type AiPrompt = {
  name: string;
  title: string;
  description?: string;
  system: string;
  user?: string;
  default?: boolean;
  builtin?: boolean;
};

type SearchResult = {
  raw?: string;
  proxies?: { host: string; port: number; protocol?: string }[];
  count?: number;
};

const JSON_LIST_KEYS = ["proxies", "hosts", "items", "list"];

function jsonListLength(value: unknown): number | null {
  if (Array.isArray(value)) return value.length;
  if (!value || typeof value !== "object") return null;
  const object = value as Record<string, unknown>;
  for (const key of JSON_LIST_KEYS) {
    if (Array.isArray(object[key])) return object[key].length;
  }
  if (object.data && typeof object.data === "object") {
    return jsonListLength(object.data);
  }
  return null;
}

function analyzeInput(raw: string): InputPreview {
  const bytes = new TextEncoder().encode(raw).length;
  const trimmed = raw.trim();
  if (!trimmed) return { format: "文本", count: 0, bytes };

  if (trimmed.startsWith("[") || trimmed.startsWith("{")) {
    try {
      const value = JSON.parse(trimmed) as unknown;
      const count = jsonListLength(value);
      if (count !== null) {
        return {
          format: "JSON",
          count,
          bytes,
          issue: count === 0 ? "JSON 列表为空" : undefined,
        };
      }
      return { format: "JSON", count: 0, bytes, issue: "未找到 proxies、hosts、items 或 list 数组" };
    } catch {
      // Bracketed IPv6 and partly typed text also start with '['. The backend
      // intentionally falls back to line mode in the same situation.
    }
  }

  const lines = new Set(
    trimmed
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter((line) => line && !line.startsWith("#")),
  );
  return { format: "文本", count: lines.size, bytes };
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`;
}

function normalizedSource(source: string): string {
  const value = source.trim();
  if (!value) return "ai-unknown";
  const lower = value.toLowerCase();
  return lower.startsWith("ai-") || lower.startsWith("ai_") ? value : `ai-${value}`;
}

function proxiesToText(proxies: { host: string; port: number; protocol?: string }[]): string {
  return proxies
    .map((p) => (p.protocol && p.protocol !== "http" ? `${p.protocol}://${p.host}:${p.port}` : `${p.host}:${p.port}`))
    .join("\n");
}

export function AIProxyPage() {
  const [text, setText] = useState("");
  const [source, setSource] = useState("ai-search");
  const [loading, setLoading] = useState(false);
  const [result, setResult] = useState<SubmitResult | null>(null);
  const { toast } = useToast();

  const [url, setUrl] = useState("");
  const [apikey, setApikey] = useState("");
  const [model, setModel] = useState("");
  const [level, setLevel] = useState(5);
  const [promptKey, setPromptKey] = useState("proxy_extract");
  const [content, setContent] = useState("");
  const [searching, setSearching] = useState(false);
  const [searchResult, setSearchResult] = useState<SearchResult | null>(null);
  const [prompts, setPrompts] = useState<AiPrompt[]>([]);
  const [editingPrompt, setEditingPrompt] = useState<AiPrompt | null>(null);

  const preview = useMemo(() => analyzeInput(text), [text]);
  const sourcePreview = useMemo(() => normalizedSource(source), [source]);
  const activePrompt = useMemo(() => prompts.find((p) => p.name === promptKey), [prompts, promptKey]);

  useEffect(() => {
    endpoints.aiSearch
      .prompts()
      .then((list) => {
        if (Array.isArray(list)) setPrompts(list as AiPrompt[]);
      })
      .catch(() => undefined);
  }, []);

  const handleSubmit = async () => {
    if (loading) return;
    if (preview.count === 0) {
      toast("请先粘贴代理列表", "error");
      return;
    }
    if (preview.bytes > 1024 * 1024) {
      toast("输入内容超过 1 MB 上限", "error");
      return;
    }
    setLoading(true);
    setResult(null);
    try {
      const data = await endpoints.aiProxy.submit(text, source);
      setResult({ ...data, source: data.source || source });
      toast(`已提交 ${data.submitted ?? preview.count} 条，净增 ${data.net_growth ?? data.added ?? 0} 条`, "success");
    } catch (err) {
      toast(err instanceof Error ? err.message : "提交失败", "error");
    } finally {
      setLoading(false);
    }
  };

  const handleSearch = async () => {
    if (searching) return;
    if (!url.trim()) {
      toast("请填写 AI 接口 URL（OpenAI 兼容 /chat/completions）", "error");
      return;
    }
    setSearching(true);
    setSearchResult(null);
    try {
      const data = await endpoints.aiSearch.run({
        url: url.trim(),
        apikey: apikey.trim(),
        model: model.trim() || undefined,
        level,
        prompt_key: promptKey,
        content: content.trim() || undefined,
      });
      setSearchResult(data);
      const found = data.proxies ?? [];
      if (found.length > 0) {
        setText((prev) => (prev.trim() ? `${prev.trim()}\n${proxiesToText(found)}` : proxiesToText(found)));
        setResult(null);
      }
      toast(`搜索完成，找到 ${found.length} 条代理${found.length > 0 ? "，已并入下方列表" : ""}`, found.length > 0 ? "success" : "error");
    } catch (err) {
      toast(err instanceof Error ? err.message : "搜索失败", "error");
    } finally {
      setSearching(false);
    }
  };

  const handleSavePrompt = async () => {
    if (!editingPrompt) return;
    try {
      await endpoints.aiSearch.updatePrompt({
        name: editingPrompt.name,
        title: editingPrompt.title,
        description: editingPrompt.description,
        system: editingPrompt.system,
        user: editingPrompt.user,
      });
      setPrompts((prev) => {
        const idx = prev.findIndex((p) => p.name === editingPrompt.name);
        const updated = { ...editingPrompt, builtin: prev[idx]?.builtin };
        if (idx >= 0) {
          const next = [...prev];
          next[idx] = updated;
          return next;
        }
        return [...prev, updated];
      });
      toast("提示词已保存", "success");
      setEditingPrompt(null);
    } catch (err) {
      toast(err instanceof Error ? err.message : "保存失败", "error");
    }
  };

  const handleResetPrompt = async (name: string) => {
    try {
      await endpoints.aiSearch.deletePrompt(name);
      const reloaded = await endpoints.aiSearch.prompts();
      if (Array.isArray(reloaded)) setPrompts(reloaded as AiPrompt[]);
      toast("已恢复默认提示词", "success");
    } catch (err) {
      toast(err instanceof Error ? err.message : "恢复失败", "error");
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title="AI 爬取入池"
        description="调用 AI/搜索接口获取候选代理，经提示词提取后预览、校验并写入免费代理池。提示词内置且可修改。"
      />

      <div className="grid gap-6 lg:grid-cols-[1fr_280px]">
        <div className="space-y-6">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Search className="h-4 w-4" />
                AI 搜索
              </CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <div className="grid gap-3 md:grid-cols-[1fr_1fr_120px]">
                <div>
                  <label className="mb-1 block text-xs font-medium text-muted-foreground">AI 接口 URL</label>
                  <Input
                    value={url}
                    onChange={(e) => setUrl(e.target.value)}
                    placeholder="https://api.openai.com/v1/chat/completions"
                  />
                </div>
                <div>
                  <label className="mb-1 block text-xs font-medium text-muted-foreground">API Key</label>
                  <Input type="password" value={apikey} onChange={(e) => setApikey(e.target.value)} placeholder="sk-..." />
                </div>
                <div>
                  <label className="mb-1 block text-xs font-medium text-muted-foreground">模型</label>
                  <Input value={model} onChange={(e) => setModel(e.target.value)} placeholder="留空用默认" />
                </div>
              </div>

              <div className="grid gap-3 md:grid-cols-[1fr_1fr]">
                <div>
                  <label className="mb-1 block text-xs font-medium text-muted-foreground">
                    思考等级：{level} / 10
                  </label>
                  <input
                    type="range"
                    min={0}
                    max={10}
                    value={level}
                    onChange={(e) => setLevel(Number(e.target.value))}
                    className="w-full accent-sky-500"
                  />
                  <div className="flex justify-between text-[10px] text-muted-foreground">
                    <span>简洁快速</span>
                    <span>深度推理</span>
                  </div>
                </div>
                <div>
                  <label className="mb-1 block text-xs font-medium text-muted-foreground">提示词模板</label>
                  <div className="flex gap-2">
                    <select
                      value={promptKey}
                      onChange={(e) => setPromptKey(e.target.value)}
                      className="w-full rounded-lg border border-white/60 bg-white/50 px-3 py-2 text-xs outline-none focus:ring-2 focus:ring-sky-400/40 dark:border-white/10 dark:bg-white/5"
                    >
                      {prompts.length === 0
                        ? [
                            { name: "proxy_extract", title: "提取免费代理列表" },
                            { name: "proxy_discover", title: "分析网页寻找代理线索" },
                            { name: "proxy_ai_generate", title: "AI 生成候选代理" },
                          ].map((p) => (
                            <option key={p.name} value={p.name}>
                              {p.title}
                            </option>
                          ))
                        : prompts.map((p) => (
                            <option key={p.name} value={p.name}>
                              {p.title}
                            </option>
                          ))}
                    </select>
                    <Button
                      variant="secondary"
                      size="sm"
                      className="px-2"
                      title="编辑提示词"
                      onClick={() => setEditingPrompt(activePrompt ? { ...activePrompt } : null)}
                    >
                      <Edit3 className="h-4 w-4" />
                    </Button>
                  </div>
                </div>
              </div>

              <div>
                <label className="mb-1 block text-xs font-medium text-muted-foreground">
                  搜索内容 / 网页内容（可选）
                </label>
                <textarea
                  value={content}
                  onChange={(e) => setContent(e.target.value)}
                  rows={4}
                  className="w-full resize-y rounded-lg border border-white/60 bg-white/50 px-3 py-2 font-mono text-xs leading-5 outline-none ring-sky-400/40 focus:ring-2 dark:border-white/10 dark:bg-white/5"
                  placeholder="粘贴需要 AI 分析/提取的网页内容或搜索关键词"
                />
              </div>

              <Button className="w-full" disabled={searching || !url.trim()} onClick={() => void handleSearch()}>
                <RefreshCw className={`h-4 w-4 ${searching ? "animate-spin" : ""}`} />
                {searching ? "AI 搜索中..." : "运行 AI 搜索"}
              </Button>

              {searchResult ? (
                <div className="space-y-2">
                  <div className="flex items-center justify-between text-xs text-muted-foreground">
                    <span>
                      找到 <span className="font-semibold text-foreground">{searchResult.count ?? (searchResult.proxies ?? []).length}</span> 条代理，
                      已并入下方列表
                    </span>
                    <Button
                      variant="ghost"
                      size="sm"
                      title="清空搜索结果（不并入列表）"
                      onClick={() => {
                        setSearchResult(null);
                        setText("");
                        setResult(null);
                      }}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                  {searchResult.raw ? (
                    <details className="rounded-lg border border-white/60 bg-white/30 p-2 text-[11px] text-muted-foreground dark:border-white/10 dark:bg-white/5">
                      <summary className="cursor-pointer">AI 原始返回</summary>
                      <pre className="mt-2 max-h-40 overflow-auto whitespace-pre-wrap">{searchResult.raw}</pre>
                    </details>
                  ) : null}
                </div>
              ) : null}

              {editingPrompt ? (
                <div className="space-y-3 rounded-lg border border-sky-400/30 bg-sky-400/5 p-3">
                  <div className="flex items-center justify-between">
                    <div className="text-xs font-medium">编辑提示词：{editingPrompt.title}</div>
                    <div className="flex gap-2">
                      <Button variant="secondary" size="sm" onClick={() => setEditingPrompt(null)}>
                        取消
                      </Button>
                      <Button size="sm" onClick={() => void handleSavePrompt()}>
                        保存
                      </Button>
                    </div>
                  </div>
                  <Input
                    value={editingPrompt.title}
                    onChange={(e) => setEditingPrompt({ ...editingPrompt, title: e.target.value })}
                    placeholder="标题"
                  />
                  <textarea
                    value={editingPrompt.system}
                    onChange={(e) => setEditingPrompt({ ...editingPrompt, system: e.target.value })}
                    rows={7}
                    className="w-full resize-y rounded-lg border border-white/60 bg-white/50 px-3 py-2 font-mono text-xs leading-5 outline-none focus:ring-2 focus:ring-sky-400/40 dark:border-white/10 dark:bg-white/5"
                    placeholder="System 提示词"
                  />
                  <textarea
                    value={editingPrompt.user ?? ""}
                    onChange={(e) => setEditingPrompt({ ...editingPrompt, user: e.target.value })}
                    rows={2}
                    className="w-full resize-y rounded-lg border border-white/60 bg-white/50 px-3 py-2 font-mono text-xs leading-5 outline-none focus:ring-2 focus:ring-sky-400/40 dark:border-white/10 dark:bg-white/5"
                    placeholder="User 消息模板（{{.Content}} 会被替换为搜索内容）"
                  />
                  {editingPrompt.builtin ? (
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => {
                        void handleResetPrompt(editingPrompt.name);
                        setEditingPrompt(null);
                      }}
                    >
                      <RefreshCw className="h-3.5 w-3.5" />
                      恢复默认
                    </Button>
                  ) : null}
                </div>
              ) : null}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>代理列表</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <textarea
                value={text}
                onChange={(e) => {
                  setText(e.target.value);
                  setResult(null);
                }}
                onKeyDown={(event) => {
                  if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
                    event.preventDefault();
                    void handleSubmit();
                  }
                }}
                aria-label="代理列表"
                placeholder={`支持格式：\n1.1.1.1:80\n2.2.2.2:443\nsocks5://1.2.3.4:1080\n\n或 JSON：\n["1.1.1.1:80","2.2.2.2:443"]\n{"proxies":["1.1.1.1:80"]}`}
                rows={12}
                className="w-full resize-y rounded-lg border border-white/60 bg-white/50 px-3 py-2 font-mono text-xs leading-5 outline-none ring-sky-400/40 focus:ring-2 dark:border-white/10 dark:bg-white/5"
              />
              <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
                <span className={preview.issue ? "text-warning" : undefined}>
                  {preview.format} · {preview.count} 条候选{preview.issue ? ` · ${preview.issue}` : ""}
                </span>
                <span className={preview.bytes > 1024 * 1024 ? "text-danger" : undefined}>
                  {formatBytes(preview.bytes)} / 1 MB
                </span>
              </div>
            </CardContent>
          </Card>
        </div>

        <div className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>提交设置</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <div>
                <label className="mb-1 block text-xs font-medium text-muted-foreground">来源标识</label>
                <Input
                  value={source}
                  onChange={(e) => {
                    setSource(e.target.value);
                    setResult(null);
                  }}
                  placeholder="ai-search"
                />
                <p className="mt-1 text-[11px] text-muted-foreground">最终来源：{sourcePreview}</p>
              </div>
              <Button
                className="w-full"
                disabled={loading || preview.count === 0 || preview.bytes > 1024 * 1024}
                onClick={() => void handleSubmit()}
              >
                <Send className="h-4 w-4" />
                {loading ? "提交中..." : "提交到池内"}
              </Button>
              <Button
                className="w-full"
                variant="secondary"
                disabled={loading || !text}
                onClick={() => {
                  setText("");
                  setResult(null);
                }}
              >
                <Trash2 className="h-4 w-4" />
                清空
              </Button>
            </CardContent>
          </Card>

          {result ? (
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <CheckCircle2 className="h-4 w-4 text-success" />
                  提交结果
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-3 text-sm">
                <div className="grid grid-cols-2 overflow-hidden rounded-lg border border-white/60 bg-white/30 dark:border-white/10 dark:bg-white/5">
                  {[
                    ["已提交", result.submitted],
                    ["新增", result.added],
                    ["净增长", result.net_growth],
                    ["重复", result.duplicates],
                    ["已拒绝", result.rejected],
                    ["被淘汰", result.evicted],
                  ].map(([label, value], index) => (
                    <div
                      key={String(label)}
                      className={`px-3 py-2 ${index % 2 === 0 ? "border-r" : ""} ${index < 4 ? "border-b" : ""}`}
                    >
                      <div className="text-[11px] text-muted-foreground">{label}</div>
                      <div className="mt-0.5 font-semibold tabular-nums">{value ?? 0}</div>
                    </div>
                  ))}
                </div>
                <div className="break-all text-xs text-muted-foreground">来源：{result.source ?? sourcePreview}</div>
                {result.raw_at_cap || result.note ? (
                  <div className="flex gap-2 rounded-lg border border-warning/30 bg-warning-bg/70 p-2.5 text-xs text-warning">
                    <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                    <span>{result.note || "原始池已达到容量上限，新增代理可能替换现有记录。"}</span>
                  </div>
                ) : null}
              </CardContent>
            </Card>
          ) : null}
        </div>
      </div>
    </div>
  );
}
