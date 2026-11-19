//! Tauri-compatible IPC over the Go GUI bridge.
//!
//! `invoke` becomes one authenticated POST per command; `listen` becomes one
//! bearer-authenticated EventSource. The page gets its token injected into
//! index.html by the server, so no second round trip is needed.

declare global {
  interface Window {
    __ARGENT_TOKEN__?: string;
  }
}

export type UnlistenFn = () => void;

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

export async function listen<T>(
  _name: string,
  handler: (event: { payload: T }) => void,
): Promise<UnlistenFn> {
  // The bridge fans out only the engine channel. Tauri-specific channels such
  // as deep-link://new-url have no HTTP carrier and stay silent.
  if (_name !== "argent://event") return () => {};

  const source = new EventSource(`/events?token=${encodeURIComponent(pageToken())}`);
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
  return () => source.close();
}
