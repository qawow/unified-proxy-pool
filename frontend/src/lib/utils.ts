import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatLatency(ms?: number | null) {
  if (ms === null || ms === undefined) return "-";
  return `${ms} ms`;
}

export function formatSpeed(mbps?: number | null) {
  if (mbps === null || mbps === undefined) return "-";
  return `${mbps.toFixed(2)} Mbps`;
}

export function formatTime(value?: string | null) {
  if (!value) return "-";
  try {
    return new Date(value).toLocaleString();
  } catch {
    return value;
  }
}

// copyText writes text to the clipboard with a visible fallback: the
// Clipboard API is only available on secure contexts, so a panel opened over
// plain HTTP on a LAN silently "copies" nothing. Returns true when the text
// actually reached the clipboard.
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* fall through to the legacy path */
  }
  try {
    const el = document.createElement("textarea");
    el.value = text;
    el.setAttribute("readonly", "");
    el.style.position = "fixed";
    el.style.opacity = "0";
    document.body.appendChild(el);
    el.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(el);
    return ok;
  } catch {
    return false;
  }
}
