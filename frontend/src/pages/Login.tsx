import { FormEvent, useState } from "react";
import { useNavigate } from "react-router-dom";
import { endpoints } from "@/api";
import { useToast } from "@/hooks/useToast";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Shield } from "lucide-react";

export function LoginPage() {
  const [password, setPassword] = useState("");
  const [loading, setLoading] = useState(false);
  const navigate = useNavigate();
  const { toast } = useToast();

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setLoading(true);
    try {
      await endpoints.auth.login(password);
      navigate("/", { replace: true });
    } catch (error) {
      toast(error instanceof Error ? error.message : "登录失败", "error");
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="flex min-h-full items-center justify-center p-4">
      <div className="glass-card w-full max-w-md p-8">
        <div className="mb-6 flex flex-col items-center text-center">
          <div className="icon-blob icon-blob-mint mb-3 h-12 w-12">
            <Shield className="h-6 w-6" />
          </div>
          <h1 className="text-xl font-bold tracking-tight">Unified Proxy Pool</h1>
          <p className="mt-1 text-sm text-muted-foreground">统一代理池管理面板</p>
        </div>
        <p className="mb-4 text-center text-sm text-muted-foreground">默认密码 admin，登录后请尽快修改。</p>
        <form className="space-y-4" onSubmit={onSubmit}>
          <Field label="管理密码">
            <Input
              type="password"
              autoFocus
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              placeholder="请输入密码"
            />
          </Field>
          <Button className="w-full" disabled={loading} type="submit">
            {loading ? "登录中..." : "登录"}
          </Button>
        </form>
      </div>
    </div>
  );
}
