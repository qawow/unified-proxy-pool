import { cn } from "@/lib/utils";
import type { ButtonHTMLAttributes } from "react";

type Props = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary" | "ghost" | "danger";
  size?: "sm" | "md";
};

export function Button({ className, variant = "primary", size = "md", ...props }: Props) {
  return (
    <button
      className={cn(
        "inline-flex items-center justify-center gap-1.5 rounded-full font-medium transition disabled:cursor-not-allowed disabled:opacity-50",
        size === "sm" ? "h-8 px-3 text-xs" : "h-9 px-4 text-sm",
        variant === "primary" &&
          "bg-gradient-to-r from-sky-500 to-teal-500 text-white shadow-md shadow-sky-500/20 hover:opacity-95",
        variant === "secondary" &&
          "border border-white/60 bg-white/70 text-foreground shadow-sm backdrop-blur hover:bg-white/90 dark:border-white/10 dark:bg-white/10",
        variant === "ghost" && "hover:bg-white/50 dark:hover:bg-white/10",
        variant === "danger" && "bg-danger text-white hover:opacity-90",
        className,
      )}
      {...props}
    />
  );
}
