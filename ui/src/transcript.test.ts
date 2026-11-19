import { describe, expect, it } from "vitest";
import type { Item } from "./ipc";
import { createStreamBuffer, stepCountsAfter, updateToolItem } from "./transcript";

const textItem = (id: number, kind: "user" | "assistant"): Item => ({ id, kind, text: "x" });
const toolItem = (id: number, toolId: string, state: "running" | "ok" = "running"): Item => ({
  id,
  kind: "tool",
  tool: { id: toolId, name: toolId, args: "", state, output: "" },
});

describe("stepCountsAfter", () => {
  it("counts only user messages strictly after each item", () => {
    const items = [
      textItem(1, "user"),
      textItem(2, "assistant"),
      textItem(3, "user"),
      toolItem(4, "t"),
      textItem(5, "user"),
    ];
    expect([...stepCountsAfter(items).entries()]).toEqual([
      [1, 2],
      [2, 2],
      [3, 1],
      [4, 1],
      [5, 0],
    ]);
  });

  it("handles empty and non-user transcripts", () => {
    expect(stepCountsAfter([]).size).toBe(0);
    const items = [toolItem(1, "a"), toolItem(2, "b")];
    expect(stepCountsAfter(items).get(1)).toBe(0);
    expect(stepCountsAfter(items).get(2)).toBe(0);
  });
});

describe("updateToolItem", () => {
  it("replaces only the matching tool and keeps every other reference", () => {
    const first = toolItem(1, "a");
    const user = textItem(2, "user");
    const second = toolItem(3, "b");
    const items = [first, user, second];

    const next = updateToolItem(items, "a", (tool) => ({
      ...tool,
      state: "ok",
      output: "done",
      duration: 12,
    }));

    expect(next).not.toBe(items);
    expect(next[0]).not.toBe(first);
    expect(next[1]).toBe(user);
    expect(next[2]).toBe(second);
    expect(next[0]).toMatchObject({
      kind: "tool",
      tool: { state: "ok", output: "done", duration: 12 },
    });
    // The original transcript is never mutated.
    expect(first).toMatchObject({ tool: { state: "running", output: "" } });
  });

  it("returns the original array when no tool matches", () => {
    const items = [textItem(1, "user")];
    const update = (tool: Extract<Item, { kind: "tool" }>["tool"]) => tool;
    expect(updateToolItem(items, "missing", update)).toBe(items);
  });
});

describe("createStreamBuffer", () => {
  type Kind = "assistant" | "thinking";

  function harness() {
    const appended: Array<[Kind, string, string]> = [];
    let scheduled = 0;
    let cancelled = 0;
    let scheduledRun: () => void = () => {};
    const buffer = createStreamBuffer<Kind>(
      (kind, text, sid) => {
        appended.push([kind, text, sid]);
      },
      (run) => {
        scheduled += 1;
        scheduledRun = run;
        return scheduled;
      },
      () => {
        cancelled += 1;
      },
    );
    return {
      buffer,
      appended,
      runScheduled: () => scheduledRun(),
      counts: () => ({ scheduled, cancelled }),
    };
  }

  it("coalesces deltas per session and kind and appends once on the tick", () => {
    const { buffer, appended, runScheduled, counts } = harness();

    buffer.push("assistant", "hel", "1");
    buffer.push("assistant", "lo", "1");
    buffer.push("thinking", "hm", "1");
    buffer.push("assistant", "world", "2");

    expect(buffer.pending).toBe(3);
    expect(counts().scheduled).toBe(1);
    expect(appended).toEqual([]);

    runScheduled();
    expect(appended).toEqual([
      ["assistant", "hello", "1"],
      ["thinking", "hm", "1"],
      ["assistant", "world", "2"],
    ]);
    expect(buffer.pending).toBe(0);
  });

  it("flush cancels the pending tick and drains immediately", () => {
    const { buffer, appended, runScheduled, counts } = harness();

    buffer.push("assistant", "a", "1");
    buffer.flush();

    expect(counts().cancelled).toBe(1);
    expect(appended).toEqual([["assistant", "a", "1"]]);
    expect(buffer.pending).toBe(0);

    // A stale tick firing later must not duplicate anything.
    runScheduled();
    expect(appended).toEqual([["assistant", "a", "1"]]);
  });
});
