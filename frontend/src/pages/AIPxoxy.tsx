import { useState } from "react";
import { endpoints } from "@/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useToast } from "@/hooks/useToast";
import { PageHeader } from "@/components/PageHeader";

export function AIPxoxy() {
  const [text, setText] = useState("");
  const [source, setSource] = useState("ai-claude");
  const [loading, setLoading] = useState(false);
  const [result, setResult] = useState<{
    submitted?: number;
    added?: number;
    duplicates?: number;
    source?: string;
    note?: string;
  } | null>(null);
  const { toast } = useToast();

  const parseLines = (raw: string): string[] => {
    const trimmed = raw.trim();
    if (!trimmed) return [];
    // JSON array
    if (trimmed.startsWith("[")) {
      try {
        const arr = JSON.parse(trimmed) as unknown;
        if (Array.isArray(arr)) {
          return arr.map((x) => String(x).trim()).filter(Boolean);
        }
      } catch {
        /* fall through to line mode */
      }
    }
    // JSON object with proxies field
    if (trimmed.startsWith("{")) {
      try {
        const obj = JSON.parse(trimmed) as { proxies?: unknown };
        if (Array.isArray(obj.proxies)) {
          return obj.proxies.map((x) => String(x).trim()).filter(Boolean);
        }
      } catch {
        /* fall through */
      }
    }
    return trimmed
      .split(/\r?\n/)
      .map((l) => l.trim())
      .filter((l) => l && !l.startsWith("#"));
  };

  const handleSubmit = async () => {
    const lines = parseLines(text);
    if (lines.length === 0) {
      toast("请先粘贴代理列表", "error");
      return;
    }
    setLoading(true);
    setResult(null);
    try {
      const data = (await endpoints.aiProxy.submit(lines, source || "ai-unknown")) as {
        submitted?: number;
        added?: number;
        duplicates?: number;
        source?: string;
        note?: string;
      };
      setResult({ ...data, source: data.source || source });
      toast(`已提交 ${data.submitted ?? lines.length} 条，新增 ${data.added ?? 0} 条`, "success");
    } catch (err) {
      toast(err instanceof Error ? err.message : "提交失败", "error");
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title="AI 爬取入池"
        description="粘贴 AI 生成的代理列表（JSON 或每行 host:port），一键加入免费代理池。"
      />

      <div className="grid gap-6 lg:grid-cols-[1fr_280px]">
        <Card>
          <CardHeader>
            <CardTitle>代理列表</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <textarea
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder={`支持格式：\n1.1.1.1:80\n2.2.2.2:443\nsocks5://1.2.3.4:1080\n\n或 JSON：\n["1.1.1.1:80","2.2.2.2:443"]\n{"proxies":["1.1.1.1:80"]}`}
              rows={16}
              className="w-full rounded-2xl border border-white/60 bg-white/50 px-3 py-2 font-mono text-xs outline-none ring-sky-400/40 focus:ring-2 dark:border-white/10 dark:bg-white/5"
            />
            <div className="text-xs text-muted-foreground">
              已识别约 {parseLines(text).length} 条 · 支持 # 注释行
            </div>
          </CardContent>
        </Card>

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
                  onChange={(e) => setSource(e.target.value)}
                  placeholder="ai-claude"
                />
                <p className="mt-1 text-[11px] text-muted-foreground">
                  入库后显示为 ai-xxx，便于按源统计质量
                </p>
              </div>
              <Button className="w-full" disabled={loading} onClick={() => void handleSubmit()}>
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
                清空
              </Button>
            </CardContent>
          </Card>

          {result ? (
            <Card>
              <CardHeader>
                <CardTitle>提交结果</CardTitle>
              </CardHeader>
              <CardContent className="space-y-1 text-sm">
                <div>解析：{result.submitted ?? "-"}</div>
                <div>新增：{result.added ?? "-"}</div>
                <div>重复：{result.duplicates ?? "-"}</div>
                <div>来源：{result.source ?? source}</div>
                {result.note ? <div className="text-xs text-muted-foreground">{result.note}</div> : null}
              </CardContent>
            </Card>
          ) : null}
        </div>
      </div>
    </div>
  );
}
