import { NavLink, Outlet, useNavigate } from "react-router-dom";
import {
  Activity,
  Boxes,
  Bot,
  Globe2,
  LayoutDashboard,
  ListTree,
  LogOut,
  Menu,
  Moon,
  Network,
  Radar,
  Settings,
  ShieldBan,
  Sun,
  X,
} from "lucide-react";
import { useState } from "react";
import { endpoints } from "@/api";
import { useTheme } from "@/hooks/useTheme";
import { useToast } from "@/hooks/useToast";
import { cn } from "@/lib/utils";
import { Button } from "./ui/button";

const navGroups = [
  {
    title: "总览",
    items: [
      { to: "/", label: "仪表盘", icon: LayoutDashboard, end: true },
      { to: "/proxies", label: "代理池", icon: Globe2 },
      { to: "/sources", label: "采集源", icon: Radar },
      { to: "/validator", label: "校验统计", icon: Activity },
      { to: "/channels", label: "渠道封禁", icon: ShieldBan },
    ],
  },
  {
    title: "节点",
    items: [
      { to: "/subscriptions", label: "订阅管理", icon: ListTree },
      { to: "/nodes", label: "手动节点", icon: Network },
      { to: "/pools", label: "出口池", icon: Boxes },
      { to: "/ai-proxy", label: "AI 爬取", icon: Bot },
    ],
  },
  {
    title: "系统",
    items: [{ to: "/settings", label: "系统设置", icon: Settings }],
  },
];

export function Layout() {
  const [collapsed, setCollapsed] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const { theme, toggle } = useTheme();
  const { toast } = useToast();
  const navigate = useNavigate();

  const logout = async () => {
    try {
      await endpoints.auth.logout();
      navigate("/login", { replace: true });
    } catch (error) {
      toast(error instanceof Error ? error.message : "logout failed", "error");
    }
  };

  const sidebar = (
    <aside
      className={cn(
        "flex h-full flex-col border-r border-white/50 bg-white/45 text-sidebar-foreground shadow-[var(--shadow-soft)] backdrop-blur-xl transition-all dark:border-white/5 dark:bg-white/5",
        collapsed ? "w-[72px]" : "w-64",
      )}
    >
      <div className="flex items-center justify-between gap-2 px-4 py-5">
        {!collapsed ? (
          <div>
            <div className="text-[15px] font-bold tracking-tight">Unified Proxy</div>
            <div className="text-xs text-muted-foreground">统一代理池面板</div>
          </div>
        ) : (
          <div className="mx-auto flex h-9 w-9 items-center justify-center rounded-2xl bg-gradient-to-br from-sky-400 to-teal-400 text-sm font-bold text-white shadow-md">
            UP
          </div>
        )}
        <button
          className="hidden rounded-xl p-1.5 text-muted-foreground hover:bg-white/60 lg:inline-flex dark:hover:bg-white/10"
          onClick={() => setCollapsed((v) => !v)}
        >
          <Menu className="h-4 w-4" />
        </button>
      </div>

      <nav className="flex-1 space-y-5 overflow-y-auto px-3 pb-3">
        {navGroups.map((group) => (
          <div key={group.title}>
            {!collapsed ? (
              <div className="mb-2 px-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground/70">
                {group.title}
              </div>
            ) : null}
            <div className="space-y-1">
              {group.items.map((item) => {
                const Icon = item.icon;
                return (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    end={item.end}
                    onClick={() => setMobileOpen(false)}
                    className={({ isActive }) =>
                      cn(
                        "flex items-center gap-2.5 rounded-2xl px-3 py-2.5 text-sm font-medium transition",
                        isActive
                          ? "bg-gradient-to-r from-sky-500/90 to-teal-500/90 text-white shadow-md shadow-sky-500/20"
                          : "text-sidebar-foreground/80 hover:bg-white/70 dark:hover:bg-white/10",
                        collapsed && "justify-center px-0",
                      )
                    }
                    title={item.label}
                  >
                    <Icon className="h-4 w-4 shrink-0" />
                    {!collapsed ? <span>{item.label}</span> : null}
                  </NavLink>
                );
              })}
            </div>
          </div>
        ))}
      </nav>

      <div className="space-y-1 border-t border-white/40 p-3 dark:border-white/5">
        <Button variant="ghost" className="w-full justify-start rounded-2xl" onClick={toggle}>
          {theme === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
          {!collapsed ? <span>{theme === "dark" ? "浅色模式" : "深色模式"}</span> : null}
        </Button>
        <Button variant="ghost" className="w-full justify-start rounded-2xl" onClick={logout}>
          <LogOut className="h-4 w-4" />
          {!collapsed ? <span>退出登录</span> : null}
        </Button>
      </div>
    </aside>
  );

  return (
    <div className="flex h-full">
      <div className="hidden lg:block">{sidebar}</div>
      {mobileOpen ? (
        <div className="fixed inset-0 z-40 flex lg:hidden">
          <div className="absolute inset-0 bg-black/30 backdrop-blur-sm" onClick={() => setMobileOpen(false)} />
          <div className="relative z-10 h-full">{sidebar}</div>
        </div>
      ) : null}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center gap-2 border-b border-white/40 bg-white/30 px-4 py-3 backdrop-blur-md lg:hidden dark:border-white/5 dark:bg-black/20">
          <Button variant="ghost" size="sm" onClick={() => setMobileOpen(true)}>
            <Menu className="h-4 w-4" />
          </Button>
          <div className="font-semibold">Unified Proxy</div>
          <div className="ml-auto">
            <Button variant="ghost" size="sm" onClick={() => setMobileOpen(false)}>
              <X className="h-4 w-4" />
            </Button>
          </div>
        </header>
        <main className="flex-1 overflow-auto p-4 md:p-6 lg:p-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
