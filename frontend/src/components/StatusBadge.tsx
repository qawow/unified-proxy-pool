import { cn } from "@/lib/utils";

const toneMap: Record<string, string> = {
  available: "bg-success-bg text-success",
  enabled: "bg-success-bg text-success",
  success: "bg-success-bg text-success",
  running: "bg-success-bg text-success",
  unavailable: "bg-danger-bg text-danger",
  error: "bg-danger-bg text-danger",
  failed: "bg-danger-bg text-danger",
  disabled: "bg-muted text-muted-foreground",
  testing: "bg-warning-bg text-warning",
  unknown: "bg-muted text-muted-foreground",
  pending: "bg-warning-bg text-warning",
  fragile: "bg-violet-100 text-violet-700 dark:bg-violet-900/40 dark:text-violet-300",
};

export function StatusBadge({ status }: { status?: string | null }) {
  const key = (status || "unknown").toLowerCase();
  return (
    <span
      className={cn(
        "inline-flex rounded-full px-2.5 py-0.5 text-xs font-semibold",
        toneMap[key] || toneMap.unknown,
      )}
    >
      {status || "unknown"}
    </span>
  );
}
