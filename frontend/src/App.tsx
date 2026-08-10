import { Navigate, Route, Routes } from "react-router-dom";
import { useEffect, useState } from "react";
import { endpoints, ApiError } from "@/api";
import { Layout } from "@/components/Layout";
import { SseProvider } from "@/components/SseProvider";
import { DashboardPage } from "@/pages/Dashboard";
import { LoginPage } from "@/pages/Login";
import { NodesPage } from "@/pages/Nodes";
import { PoolsPage } from "@/pages/Pools";
import { ProxiesPage } from "@/pages/Proxies";
import { SettingsPage } from "@/pages/Settings";
import { SourcesPage } from "@/pages/Sources";
import { SubscriptionDetailPage } from "@/pages/SubscriptionDetail";
import { SubscriptionsPage } from "@/pages/Subscriptions";
import { ValidatorPage } from "@/pages/Validator";
import { AIProxyPage } from "@/pages/AIProxy";
import { Button } from "@/components/ui/button";

function Protected({ children }: { children: React.ReactNode }) {
  const [state, setState] = useState<"loading" | "ok" | "no" | "error">("loading");
  const [error, setError] = useState("");

  const check = () => {
    setState("loading");
    setError("");
    endpoints.auth.me()
      .then(() => setState("ok"))
      .catch((err) => {
        if (err instanceof ApiError && err.status === 401) {
          setState("no");
          return;
        }
        setError(err instanceof Error ? err.message : "网络错误");
        setState("error");
      });
  };

  useEffect(() => {
    check();
  }, []);

  if (state === "loading") {
    return <div className="flex h-full items-center justify-center text-sm text-muted-foreground">加载中...</div>;
  }
  if (state === "no") return <Navigate to="/login" replace />;
  if (state === "error") {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 text-sm">
        <div className="text-danger">无法连接服务：{error}</div>
        <Button onClick={check}>重试</Button>
      </div>
    );
  }
  return <SseProvider enabled>{children}</SseProvider>;
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        path="/"
        element={
          <Protected>
            <Layout />
          </Protected>
        }
      >
        <Route index element={<DashboardPage />} />
        <Route path="proxies" element={<ProxiesPage />} />
        <Route path="sources" element={<SourcesPage />} />
        <Route path="validator" element={<ValidatorPage />} />
        <Route path="subscriptions" element={<SubscriptionsPage />} />
        <Route path="subscriptions/:id" element={<SubscriptionDetailPage />} />
        <Route path="nodes" element={<NodesPage />} />
        <Route path="manual-nodes" element={<Navigate to="/nodes" replace />} />
        <Route path="pools" element={<PoolsPage />} />
        <Route path="ai-proxy" element={<AIProxyPage />} />
        <Route path="settings" element={<SettingsPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
