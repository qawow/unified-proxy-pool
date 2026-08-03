import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import { cn } from "@/lib/utils";

type ToastTone = "info" | "success" | "error";

type ToastItem = {
  id: number;
  message: string;
  tone: ToastTone;
  copied?: boolean;
};

type ToastContextValue = {
  toast: (message: string, tone?: ToastTone) => void;
};

const ToastContext = createContext<ToastContextValue | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const timers = useRef<Map<number, ReturnType<typeof setTimeout>>>(new Map());

  const removeLater = useCallback((id: number, ms = 4200) => {
    const prev = timers.current.get(id);
    if (prev) clearTimeout(prev);
    const t = setTimeout(() => {
      setItems((list) => list.filter((item) => item.id !== id));
      timers.current.delete(id);
    }, ms);
    timers.current.set(id, t);
  }, []);

  const toast = useCallback(
    (message: string, tone: ToastTone = "info") => {
      const id = Date.now() + Math.random();
      setItems((prev) => [...prev, { id, message, tone }]);
      removeLater(id, 4200);
    },
    [removeLater],
  );

  const copyToast = useCallback(
    async (item: ToastItem) => {
      try {
        await navigator.clipboard.writeText(item.message);
        setItems((prev) => prev.map((t) => (t.id === item.id ? { ...t, copied: true } : t)));
        removeLater(item.id, 1800);
        window.setTimeout(() => {
          setItems((prev) => prev.map((t) => (t.id === item.id ? { ...t, copied: false } : t)));
        }, 1200);
      } catch {
        // fallback: select text via temporary textarea
        const el = document.createElement("textarea");
        el.value = item.message;
        document.body.appendChild(el);
        el.select();
        try {
          document.execCommand("copy");
          setItems((prev) => prev.map((t) => (t.id === item.id ? { ...t, copied: true } : t)));
          removeLater(item.id, 1800);
        } finally {
          document.body.removeChild(el);
        }
      }
    },
    [removeLater],
  );

  const value = useMemo(() => ({ toast }), [toast]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="pointer-events-none fixed right-4 top-4 z-[100] flex w-[min(22rem,calc(100vw-2rem))] flex-col gap-2">
        {items.map((item) => (
          <div
            key={item.id}
            role="status"
            title="点击复制内容"
            onMouseEnter={() => {
              const t = timers.current.get(item.id);
              if (t) clearTimeout(t);
            }}
            onMouseLeave={() => removeLater(item.id, 2200)}
            onClick={() => void copyToast(item)}
            className={cn(
              "pointer-events-auto cursor-pointer select-text rounded-2xl border px-3 py-2.5 text-sm shadow-lg backdrop-blur-md transition hover:brightness-[0.98] active:scale-[0.99]",
              item.tone === "success" && "border-success/30 bg-success-bg/95 text-success",
              item.tone === "error" && "border-danger/30 bg-danger-bg/95 text-danger",
              item.tone === "info" && "border-white/60 bg-white/90 text-foreground dark:border-white/10 dark:bg-card/95",
            )}
          >
            <div className="flex items-start gap-2">
              <div className="min-w-0 flex-1 break-words whitespace-pre-wrap">{item.message}</div>
              <button
                type="button"
                className={cn(
                  "shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium",
                  item.copied ? "bg-black/10" : "bg-black/5 hover:bg-black/10 dark:bg-white/10 dark:hover:bg-white/15",
                )}
                onClick={(e) => {
                  e.stopPropagation();
                  void copyToast(item);
                }}
              >
                {item.copied ? "已复制" : "复制"}
              </button>
            </div>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast() {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error("useToast must be used within ToastProvider");
  return ctx;
}
