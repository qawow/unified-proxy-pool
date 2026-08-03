import { createContext, useContext, useEffect, useMemo, useRef, type ReactNode } from "react";

type Handler = (event: { type?: string; [key: string]: unknown }) => void;

type SseContextValue = {
  subscribe: (handler: Handler) => () => void;
};

const SseContext = createContext<SseContextValue | null>(null);

export function SseProvider({ children, enabled }: { children: ReactNode; enabled: boolean }) {
  const handlers = useRef(new Set<Handler>());

  useEffect(() => {
    if (!enabled || !window.EventSource) return;
    const es = new EventSource("/api/events");
    es.onmessage = (raw) => {
      try {
        const data = JSON.parse(raw.data);
        handlers.current.forEach((handler) => handler(data));
      } catch {
        // ignore malformed events
      }
    };
    return () => es.close();
  }, [enabled]);

  const value = useMemo<SseContextValue>(
    () => ({
      subscribe: (handler) => {
        handlers.current.add(handler);
        return () => handlers.current.delete(handler);
      },
    }),
    [],
  );

  return <SseContext.Provider value={value}>{children}</SseContext.Provider>;
}

/** Debounced SSE subscription to avoid reload storms during validate batches. */
export function useSse(handler: Handler, deps: unknown[] = [], debounceMs = 800) {
  const ctx = useContext(SseContext);
  const handlerRef = useRef(handler);
  handlerRef.current = handler;

  useEffect(() => {
    if (!ctx) return;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const wrapped: Handler = (event) => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => {
        handlerRef.current(event);
      }, debounceMs);
    };
    const unsub = ctx.subscribe(wrapped);
    return () => {
      if (timer) clearTimeout(timer);
      unsub();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ctx, debounceMs, ...deps]);
}
