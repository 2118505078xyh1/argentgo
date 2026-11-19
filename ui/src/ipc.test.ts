import { describe, expect, it } from "vitest";
import { asNumber, asRecord, asString, parseIncoming, parseReady } from "./ipc";

describe("IPC decoders", () => {
  it("coerces unknown payloads without throwing", () => {
    expect(asString(42, "fallback")).toBe("fallback");
    expect(asNumber(Number.NaN)).toBeUndefined();
    expect(asRecord([])).toBeNull();
  });

  it("keeps model windows and engine-provided thinking levels", () => {
    const ready = parseReady({
      model: "e2e-model",
      level: "low",
      contextMax: 4096,
      cwd: "/work",
      provider: "local",
      protocol: "openai-chat-completions",
      models: [
        { id: "a", contextLength: 960_000 },
        { id: "", contextLength: 1 },
        "not-an-object",
      ],
      thinking: ["low", "high", 7],
    });

    expect(ready.model).toBe("e2e-model");
    expect(ready.models).toEqual([{ id: "a", contextLength: 960_000 }]);
    expect(ready.thinkingLevels).toEqual(["low", "high"]);
    expect(ready.contextMax).toBe(4096);
  });
});

describe("parseIncoming", () => {
  it("decodes every event kind the backend emits", () => {
    expect(parseIncoming({ kind: "session_cleared" })).toEqual({ kind: "session_cleared" });
    expect(parseIncoming({ kind: "session", path: "a.jsonl", sid: "2" })).toEqual({
      kind: "session",
      path: "a.jsonl",
    });
    expect(parseIncoming({ kind: "error", message: "boom" })).toEqual({
      kind: "error",
      message: "boom",
    });
    expect(
      parseIncoming({
        kind: "approval",
        sid: "1",
        id: 7,
        name: "write",
        arguments: "diff",
      }),
    ).toMatchObject({ kind: "approval", id: 7, name: "write" });
    expect(
      parseIncoming({
        kind: "turn_done",
        reason: "complete",
        promptTokens: 12,
        completionTokens: 3,
        totalTokens: 15,
        cachedTokens: 4,
        requests: 2,
        cost: 0.5,
        priced: true,
      }),
    ).toMatchObject({ kind: "turn_done", promptTokens: 12, cost: 0.5, priced: true });
  });

  it("rebuilds loaded rows and agent tool events", () => {
    const loaded = parseIncoming({
      kind: "loaded",
      entries: [
        { kind: "user", text: "hi" },
        { kind: "notice", level: "warning", text: "careful" },
        {
          kind: "tool",
          callId: "c1",
          name: "read",
          arguments: "{}",
          output: "ok",
          state: "ok",
          durationMs: 12,
        },
        { kind: "unknown" },
      ],
    });
    expect(loaded?.kind).toBe("loaded");
    if (loaded?.kind === "loaded") {
      expect(loaded.entries).toHaveLength(3);
      expect(loaded.entries[1]).toMatchObject({ kind: "notice", level: "warning" });
      expect(loaded.entries[2]).toMatchObject({ kind: "tool", callId: "c1", state: "ok" });
    }

    const tool = parseIncoming({
      kind: "agent",
      event: {
        type: "tool_finished",
        id: "c1",
        name: "read",
        output: "done",
        isError: false,
        durationMs: 8,
      },
    });
    expect(tool).toMatchObject({
      kind: "agent",
      event: { type: "tool_finished", durationMs: 8 },
    });
  });

  it("rejects malformed payloads instead of trusting them", () => {
    expect(parseIncoming(null)).toBeNull();
    expect(parseIncoming([])).toBeNull();
    expect(parseIncoming({ kind: "nope" })).toBeNull();
    expect(parseIncoming({ kind: "agent", event: { type: "nope" } })).toBeNull();
  });
});
