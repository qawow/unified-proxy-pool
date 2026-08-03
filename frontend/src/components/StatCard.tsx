import { cn } from "@/lib/utils";
import type { LucideIcon } from "lucide-react";

const toneBlob: Record<string, string> = {
  default: "icon-blob-sky",
  success: "icon-blob-mint",
  warning: "icon-blob-amber",
  danger: "icon-blob-violet",
  amber: "icon-blob-amber",
  mint: "icon-blob-mint",
  sky: "icon-blob-sky",
  violet: "icon-blob-violet",
};

export function StatCard({
  title,
  value,
  hint,
  icon: Icon,
  tone = "default",
  badge,
  className,
}: {
  title: string;
  value: string | number;
  hint?: string;
  icon?: LucideIcon;
  tone?: "default" | "success" | "warning" | "danger" | "amber" | "mint" | "sky" | "violet";
  badge?: string;
  className?: string;
}) {
  return (
    <div className={cn("glass-card relative p-5 transition hover:shadow-[var(--shadow-card)]", className)}>
      {badge ? (
        <div className="absolute right-4 top-4">
          <span className="soft-pill">{badge}</span>
        </div>
      ) : null}
      <div className="flex items-start gap-3">
        {Icon ? (
          <div className={cn("icon-blob", toneBlob[tone] || toneBlob.default)}>
            <Icon className="h-5 w-5" strokeWidth={2.2} />
          </div>
        ) : null}
        <div className="min-w-0 flex-1 pr-10">
          <div className="text-[13px] font-medium text-muted-foreground">{title}</div>
          <div className="mt-1.5 text-[1.75rem] font-bold leading-none tracking-tight text-foreground tabular-nums">
            {value}
          </div>
          {hint ? <div className="mt-2 text-xs text-muted-foreground/90">{hint}</div> : null}
        </div>
      </div>
    </div>
  );
}
