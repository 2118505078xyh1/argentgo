//! Pure transcript helpers. Kept DOM-free so they can be unit-tested and
//! reused without pulling in a component or a WebView.

import type { Item } from "./ipc";
import type { Tool } from "./ToolCard";

/**
 * Number of `user` items strictly after each item, keyed by item id.
 * Two forward passes keep this O(n) instead of slicing the tail for every row.
 */
export function stepCountsAfter(items: readonly Item[]): Map<number, number> {
  const counts = new Map<number, number>();
  let usersAfter = 0;
  for (const item of items) if (item.kind === "user") usersAfter += 1;
  // Keep insertion order aligned with `items`; decrement as we pass each user.
  for (const item of items) {
    if (item.kind === "user") usersAfter -= 1;
    counts.set(item.id, usersAfter);
  }
  return counts;
}

/**
 * Replace just the matching tool item, preserving every other element by
 * reference. Returns the original array when nothing matches so Solid can
 * skip re-rendering the transcript entirely.
 */
export function updateToolItem(
  items: Item[],
  toolId: string,
  update: (tool: Tool) => Tool,
): Item[] {
  const index = items.findIndex((item) => item.kind === "tool" && item.tool.id === toolId);
  if (index < 0) return items;
  const previous = items[index];
  if (previous.kind !== "tool") return items; // Type narrowing only.
  const next = items.slice();
  next[index] = { ...previous, tool: update(previous.tool) };
  return next;
}

export type ScheduleFlush = (run: () => void) => unknown;
export type CancelFlush = (handle: unknown) => void;

export type StreamBuffer<Kind extends string> = {
  push: (kind: Kind, text: string, sid: string) => void;
  /** Flush immediately, cancelling any pending scheduled tick. */
  flush: () => void;
  readonly pending: number;
};

/**
 * Coalesces token deltas by (session, kind) and appends them at most once per
 * scheduled tick. `schedule`/`cancel` are injected so this stays testable
 * without a DOM; the app passes requestAnimationFrame/cancelAnimationFrame.
 */
export function createStreamBuffer<Kind extends string>(
  append: (kind: Kind, text: string, sid: string) => void,
  schedule: ScheduleFlush,
  cancel: CancelFlush,
): StreamBuffer<Kind> {
  type Pending = { kind: Kind; text: string; sid: string };
  const buffer = new Map<string, Pending>();
  let handle: unknown = null;
  let scheduled = false;

  const drain = () => {
    scheduled = false;
    handle = null;
    if (buffer.size === 0) return;
    // Snapshot before appending so a re-entrant push schedules a fresh tick.
    const entries = Array.from(buffer.values());
    buffer.clear();
    for (const entry of entries) append(entry.kind, entry.text, entry.sid);
  };

  return {
    push(kind, text, sid) {
      const key = `${sid}:${kind}`;
      const pending = buffer.get(key);
      if (pending) pending.text += text;
      else buffer.set(key, { kind, text, sid });
      if (!scheduled) {
        scheduled = true;
        handle = schedule(drain);
      }
    },
    flush() {
      if (scheduled) {
        cancel(handle);
        scheduled = false;
        handle = null;
      }
      drain();
    },
    get pending() {
      return buffer.size;
    },
  };
}
