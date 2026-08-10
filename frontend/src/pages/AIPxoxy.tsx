// src/pages/AIPxoxy.tsx
import { useState } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { toast } from "@/hooks/useToast";
import { endpoints } from "@/api";
import { PageHeader } from "@/components/PageHeader";

export function AIPxoxy() {
  const [tool, setTool] = useState("web");
  const [url, setUrl] = useState("");
  const [apikey, setApikey] = useState("");
  const [level, setLevel] = useState(7);
  const [text, setText] = useState("");
  const [loading, setLoading] = useState(false);
  const [result, setResult] = useState<{ submitted?: number; added?: number; duplicates?: number; source?: string; note?: string } | null>(null);
  const { toast } = useToast();

  const handleSearch = async () => {
    if (!url.trim() || !apikey.trim()) {
      toast("请填写 URL 和 API Key", "error");
      return;
    }
    setLoading(true);
    try {
      const res = await fetch(`/api/ai-proxy?source=ai-${tool}&level=${level}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ url, apikey }),
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.message || "搜索失败");
      setResult(data.data);
      toast("搜索完成，已解析代理", "success");
    } catch (err) {
      toast(err instanceof Error ? err.message : "搜索失败", "error");
    } finally {
      setLoading(false);
    }
  };

  const handleSubmit = async () => {
    if (!result) return;
    setLoading(true);
    try {
      const res = await endpoints.aiProxy.submit(result.proxies || [], "ai-search");
      toast(`提交成功：新增 ${res.added ?? 0} 条`, "success");
      setResult(null);
    } catch (err) {
      toast(err instanceof Error ? err.message : "提交失败", "error");
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader title="AI 爬取代理" description="Web 搜索 + AI 代理生成 + 思考等级调节" />

      <div className="grid gap-6 lg:grid-cols-[1fr_320px]">
        <Card>
          <CardHeader>
            <CardTitle>搜索配置</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div>
              <label className="text-xs font-medium mb-1 block">工具</label>
              <select value={tool} onChange={(e) => setTool(e.target.value)} className="w-full rounded-2xl border p-3">
                <option value="web">Web 搜索</option>
                <option value="google">Google</option>
                <option value="bing">Bing</option>
                <option value="custom">自定义爬虫</option>
              </select>
            </div>

            <div>
              <label className="text-xs font-medium mb-1 block">URL</label>
              <input type="text" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://..." className="w-full rounded-2xl border p-3" />
            </div>

            <div>
              <label className="text-xs font-medium mb-1 block">API Key</label>
              <input type="text" value={apikey} onChange={(e) => setApikey(e.target.value)} placeholder="sk-..." className="w-full rounded-2xl border p-3" />
            </div>

            <div>
              <label className="text-xs font-medium mb-2 block">思考等级 <span className="text-sky-500">{level}/10</span></label>
              <input type="range" min="0" max="10" value={level} onChange={(e) => setLevel(+e.target.value)} className="w-full" />
              <div className="text-xs text-muted-foreground mt-1">0=轻度 10=极深</div>
            </div>

            <Button className="w-full" onClick={handleSearch} loading={loading}>
              开始搜索代理
            </Button>
          </CardContent>
        </Card>

        <div className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>代理列表</CardTitle>
            </CardHeader>
            <CardContent>
              <textarea value={text} onChange={(e) => setText(e.target.value)} rows={12} className="w-full font-mono text-xs" placeholder="搜索结果将在这里显示..." />
            </CardContent>
          </Card>

          {result ? (
            <Card>
              <CardHeader>
                <CardTitle>提交结果</CardTitle>
              </CardHeader>
              <CardContent>
                <div>解析：{result.submitted ?? "-"}</div>
                <div>新增：{result.added ?? "-"}</div>
                <div>重复：{result.duplicates ?? "-"}</div>
                <Button className="w-full mt-4" onClick={handleSubmit} loading={loading}>
                  提交到池内
                </Button>
              </CardContent>
            </Card>
          ) : null}
        </div>
      </div>
    </div>
  );
}