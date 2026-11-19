import { afterEach, describe, expect, it, vi } from "vitest";
import { attemptLabel, parseAttempt } from "./attempts";
import { COMMAND_TEMPLATES, QUICK_ACTIONS, matchCommandTemplates } from "./commands";
import { parseAgentEvent, parseLoadedEntry } from "./ipc";
import { backoffDelay, listen } from "./ipc-bridge";
import { prefersReducedMotion, scrollBehavior } from "./motion";
import { isToolExpanded, readToolExpansion, toggleToolExpansion } from "./tool-state";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("reduced motion (E12)", () => {
  it("honours the media query and keeps scrolling instant when requested", () => {
    vi.stubGlobal("window", {
      matchMedia: (query: string) => ({ matches: query.includes("prefers-reduced-motion") }),
    });
    expect(prefersReducedMotion()).toBe(true);
    expect(scrollBehavior(true)).toBe("auto");
    expect(scrollBehavior(false)).toBe("smooth");
  });

  it("is false without a WebView or when matchMedia throws", () => {
    vi.stubGlobal("window", { matchMedia: () => { throw new Error("unsupported"); } });
    expect(prefersReducedMotion()).toBe(false);
  });
});

describe("per-tab tool expansion (E15)", () => {
  it("ignores corrupt persisted data", () => {
    expect(readToolExpansion("not json")).toEqual({});
    expect(readToolExpansion('{"1":{"a":true,"b":"no"}}')).toEqual({ "1": { a: true } });
  });

  it("keeps tabs independent", () => {
    const first = toggleToolExpansion({}, "1", "tool-a");
    const second = toggleToolExpansion(first, "2", "tool-a");
    expect(isToolExpanded(first, "1", "tool-a")).toBe(true);
    expect(isToolExpanded(second, "2", "tool-a")).toBe(true);
    expect(isToolExpanded(second, "1", "tool-a")).toBe(true);
    const collapsed = toggleToolExpansion(second, "1", "tool-a");
    expect(isToolExpanded(collapsed, "1", "tool-a")).toBe(false);
    expect(isToolExpanded(collapsed, "2", "tool-a")).toBe(true);
  });
});

describe("command templates and quick actions (E10, E14)", () => {
  it("matches undo/retry templates by name and argument hint", () => {
    expect(matchCommandTemplates("/und").map((item) => item.name)).toContain("/undo");
    expect(matchCommandTemplates("fork").map((item) => item.name)).toContain("/retry");
    expect(COMMAND_TEMPLATES.some((item) => item.template.includes("$ARGUMENTS"))).toBe(true);
  });

  it("offers non-empty prompts for the empty state", () => {
    expect(QUICK_ACTIONS.length).toBeGreaterThanOrEqual(4);
    for (const action of QUICK_ACTIONS) {
      expect(action.prompt.trim().length).toBeGreaterThan(0);
      expect(action.label.zh.length).toBeGreaterThan(0);
      expect(action.label.en.length).toBeGreaterThan(0);
    }
  });
});

describe("attempt metadata (E07)", () => {
  it("parses and localises attempt kinds", () => {
    expect(parseAttempt("stream_error")).toBe("stream_error");
    expect(parseAttempt("unknown")).toBeUndefined();
    expect(attemptLabel("failed", "zh")).toBe("失败");
    expect(attemptLabel("retried", "en")).toBe("retried");
  });

  it("decodes attempts on streamed and loaded rows", () => {
    const streamed = parseAgentEvent({ type: "tool_finished", id: "1", attempt: "failed" });
    expect(streamed?.type === "tool_finished" && streamed.attempt).toBe("failed");
    const loaded = parseLoadedEntry({ kind: "tool", callId: "1", attempt: "cancelled" });
    expect(loaded?.kind === "tool" && loaded.attempt).toBe("cancelled");
  });
});

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((msg: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }
  close() {
    this.closed = true;
  }
}

describe("SSE reconnect (E16)", () => {
  it("backs off exponentially with bounded jitter", () => {
    expect(backoffDelay(0, 500, 5000, 0)).toBe(375);
    expect(backoffDelay(2, 500, 5000, 1)).toBe(2000);
    expect(backoffDelay(9, 500, 5000, 1)).toBe(5000);
  });

  it("reopens the stream after an error and stops on unlisten", async () => {
    FakeEventSource.instances = [];
    vi.stubGlobal("window", { __ARGENT_TOKEN__: "tok" });
    vi.stubGlobal("EventSource", FakeEventSource);
    const timers: Array<{ run: () => void; delay: number }> = [];
    const unlisten = await listen<{ kind: string }>(
      "argent://event",
      () => {},
      {
        schedule: (run, delay) => { timers.push({ run, delay }); return timers.length; },
        cancel: () => {},
        jitter: () => 1,
      },
    );
    expect(FakeEventSource.instances).toHaveLength(1);
    FakeEventSource.instances[0].onerror?.();
    expect(FakeEventSource.instances[0].closed).toBe(true);
    expect(timers).toHaveLength(1);
    expect(timers[0].delay).toBe(500);
    timers[0].run();
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(FakeEventSource.instances[1].url).toBe("/events?token=tok");
    unlisten();
    expect(FakeEventSource.instances[1].closed).toBe(true);
  });
});
