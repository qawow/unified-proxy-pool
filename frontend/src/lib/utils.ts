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
