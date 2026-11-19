//! Tauri-compatible IPC over the Go GUI bridge.
//!
//! `invoke` becomes one authenticated POST per command; `listen` becomes one
//! bearer-authenticated EventSource. The page gets its token injected into
//! index.html by the server, so no second round trip is needed.
//!
//! EventSource reconnects on its own, but it reuses one URL and has no cap.
//! This wrapper closes on error and reopens with exponential backoff so a
//! restarted GUI server is picked up without a manual page reload.

declare global {
  interface Window {
    __ARGENT_TOKEN__?: string;
  }
}

export type UnlistenFn = () => void;

export type ListenOptions = {
  retryBaseMs?: number;
  retryMaxMs?: number;
  /** Injected for tests; defaults to setTimeout / clearTimeout. */
  schedule?: (run: () => void, delay: number) => unknown;
  cancel?: (handle: unknown) => void;
  jitter?: () => number;
};

function pageToken(): string {
  return typeof window === "undefined" ? "" : (window.__ARGENT_TOKEN__ ?? "");
}

function errorText(body: unknown, status: number, statusText: string): string {
  if (body && typeof body === "object") {
    const error = (body as { error?: unknown }).error;
    if (typeof error === "string" && error.length > 0) return error;
  }
  return `HTTP ${status}${statusText ? ` ${statusText}` : ""}`;
}

export async function invoke<T>(cmd: string, args?: unknown): Promise<T> {
  const response = await fetch(`/rpc/${encodeURIComponent(cmd)}`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${pageToken()}`,
    },
    body: JSON.stringify(args ?? {}),
  });

  let body: unknown = null;
  try {
    body = await response.json();
  } catch {
    // A non-JSON failure still needs a useful error message below.
  }

  // The Go bridge returns command failures as 200 + {"error": ...} because
  // Tauri's invoke rejects its promise; keep that contract for the page.
  if (!response.ok || (body && typeof body === "object" && "error" in body)) {
    throw new Error(errorText(body, response.status, response.statusText));
  }
  return body as T;
}

export function backoffDelay(
  attempt: number,
  baseMs: number,
  maxMs: number,
  jitter: number,
): number {
  const exponential = Math.min(maxMs, baseMs * 2 ** Math.max(attempt, 0));
  return Math.round(exponential * (0.75 + 0.25 * jitter));
}

export async function listen<T>(
  _name: string,
  handler: (event: { payload: T }) => void,
  options: ListenOptions = {},
): Promise<UnlistenFn> {
  // The bridge fans out only the engine channel. Tauri-specific channels such
  // as deep-link://new-url have no HTTP carrier and stay silent.
  if (_name !== "argent://event") return () => {};

  const baseMs = options.retryBaseMs ?? 500;
  const maxMs = options.retryMaxMs ?? 15_000;
  const schedule =
    options.schedule ?? ((run: () => void, delay: number) => setTimeout(run, delay));
  const cancel =
    options.cancel ?? ((handle: unknown) => clearTimeout(handle as ReturnType<typeof setTimeout>));
  const jitter = options.jitter ?? Math.random;

  let closed = false;
  let attempt = 0;
  let source: EventSource | null = null;
  let timer: unknown = null;

  const reconnect = () => {
    if (closed) return;
    const delay = backoffDelay(attempt, baseMs, maxMs, jitter());
    attempt += 1;
    timer = schedule(() => {
      timer = null;
      connect();
    }, delay);
  };

  const connect = () => {
    if (closed) return;
    source = new EventSource(`/events?token=${encodeURIComponent(pageToken())}`);
    source.onopen = () => {
      attempt = 0;
    };
    source.onmessage = (msg: MessageEvent<string>) => {
      let payload: T;
      try {
        payload = JSON.parse(msg.data) as T;
      } catch {
        // Ignore keepalives and malformed frames rather than breaking the stream.
        return;
      }
      handler({ payload });
    };
    source.onerror = () => {
      if (closed) return;
      source?.close();
      source = null;
      reconnect();
    };
  };

  connect();
  return () => {
    closed = true;
    if (timer !== null) {
      cancel(timer);
      timer = null;
    }
    source?.close();
    source = null;
  };
}
