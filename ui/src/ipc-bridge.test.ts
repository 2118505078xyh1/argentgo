import { afterEach, describe, expect, it, vi } from "vitest";
import { invoke, listen } from "./ipc-bridge";

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static reset() {
    FakeEventSource.instances = [];
  }

  onmessage: ((msg: { data: string }) => void) | null = null;
  closed = false;
  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }
  close() {
    this.closed = true;
  }
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("HTTP IPC bridge", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    FakeEventSource.reset();
  });

  it("posts JSON arguments with the injected bearer token", async () => {
    vi.stubGlobal("window", { __ARGENT_TOKEN__: "tok-123" });
    const fetchMock = vi.fn(async (_url: string, _init: RequestInit) =>
      jsonResponse({ sid: "1", channel: "stable" }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(invoke("get_state")).resolves.toEqual({ sid: "1", channel: "stable" });

    expect(fetchMock).toHaveBeenCalledOnce();
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/rpc/get_state");
    expect(init.method).toBe("POST");
    expect(init.headers).toMatchObject({
      "Content-Type": "application/json",
      Authorization: "Bearer tok-123",
    });
    expect(JSON.parse(init.body as string)).toEqual({});
  });

  it("serializes command arguments as the JSON request body", async () => {
    vi.stubGlobal("window", { __ARGENT_TOKEN__: "tok-123" });
    const fetchMock = vi.fn(async (_url: string, _init: RequestInit) => jsonResponse(null));
    vi.stubGlobal("fetch", fetchMock);

    await invoke("send_prompt", { sid: "2", text: "你好", steps: 3 });

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/rpc/send_prompt");
    expect(JSON.parse(init.body as string)).toEqual({ sid: "2", text: "你好", steps: 3 });
  });

  it("rejects non-2xx responses using the server error field", async () => {
    vi.stubGlobal("window", { __ARGENT_TOKEN__: "tok-123" });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, _init: RequestInit) => jsonResponse({ error: "unauthorized" }, 401)),
    );

    await expect(invoke("get_state")).rejects.toThrow("unauthorized");
  });

  it("rejects command failures returned as 200 with an error field", async () => {
    vi.stubGlobal("window", { __ARGENT_TOKEN__: "tok-123" });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, _init: RequestInit) =>
        jsonResponse({ error: "not supported in the Go build: pty host" }),
      ),
    );

    await expect(invoke("pty_spawn")).rejects.toThrow("not supported in the Go build: pty host");
  });

  it("streams parsed engine frames from the main channel", async () => {
    vi.stubGlobal("window", { __ARGENT_TOKEN__: "tok-456" });
    vi.stubGlobal("EventSource", FakeEventSource);
    const handler = vi.fn();

    const unlisten = await listen<{ kind: string }>("argent://event", handler);

    expect(FakeEventSource.instances).toHaveLength(1);
    const source = FakeEventSource.instances[0];
    expect(source.url).toBe("/events?token=tok-456");

    source.onmessage?.({ data: JSON.stringify({ kind: "ready", model: "m" }) });
    source.onmessage?.({ data: "not json" });
    expect(handler).toHaveBeenCalledTimes(1);
    expect(handler).toHaveBeenCalledWith({ payload: { kind: "ready", model: "m" } });

    unlisten();
    expect(source.closed).toBe(true);
  });

  it("ignores Tauri-only channels without opening a stream", async () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const handler = vi.fn();

    const unlisten = await listen("deep-link://new-url", handler);

    expect(FakeEventSource.instances).toHaveLength(0);
    expect(handler).not.toHaveBeenCalled();
    expect(typeof unlisten).toBe("function");
  });
});
