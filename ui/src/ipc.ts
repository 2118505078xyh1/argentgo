//! Pure IPC decoding helpers. Kept out of the component so they can be tested
//! without a WebView.

import type { Tool } from "./ToolCard";

export type UnknownRecord = Record<string, unknown>;

export type ModelInfo = { id: string; contextLength: number };

export type Ready = {
  model: string;
  level: string;
  models: ModelInfo[];
  thinkingLevels: string[];
  contextMax: number;
  cwd: string;
  provider: string;
  protocol: string;
};

export const THINKING_LEVELS = [
  "auto",
  "off",
  "minimal",
  "low",
  "medium",
  "high",
  "xhigh",
  "max",
];

export function asRecord(value: unknown): UnknownRecord | null {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as UnknownRecord)
    : null;
}

export function asString(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

export function asNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

export function parseReady(raw: UnknownRecord): Ready {
  return {
    model: asString(raw.model),
    level: asString(raw.level),
    models: Array.isArray(raw.models)
      ? raw.models
          .map((value) => asRecord(value))
          .filter((value): value is UnknownRecord => value !== null)
          .map((value) => ({
            id: asString(value.id),
            contextLength: asNumber(value.contextLength) ?? 0,
          }))
          .filter((value) => value.id.length > 0)
      : [],
    thinkingLevels: Array.isArray(raw.thinking)
      ? raw.thinking.filter((value): value is string => typeof value === "string")
      : [],
    contextMax: asNumber(raw.contextMax) ?? 0,
    cwd: asString(raw.cwd),
    provider: asString(raw.provider),
    protocol: asString(raw.protocol),
  };
}

export type TextItem = {
  id: number;
  kind: "user" | "assistant" | "thinking" | "notice";
  text: string;
};

export type ToolItem = { id: number; kind: "tool"; tool: Tool };
export type Item = TextItem | ToolItem;

export type Approval = { id: number; name: string; arguments: string; sid?: string };

export type SessionRow = {
  path: string;
  title: string;
  updated: string;
  cwd: string;
};

export type LoadedEntry =
  | { kind: "user" | "assistant" | "thinking"; text: string }
  | { kind: "notice"; level: "info" | "warning" | "error"; text: string }
  | {
      kind: "tool";
      callId: string;
      name: string;
      arguments: string;
      output: string;
      state: "running" | "ok" | "error";
      durationMs?: number;
    };

export type AgentEvent =
  | { type: "turn_start" }
  | { type: "turn_end" }
  | { type: "assistant_text"; text: string }
  | { type: "thinking"; text: string }
  | { type: "tool_requested"; id: string; name: string; arguments: string }
  | {
      type: "tool_finished";
      id: string;
      name: string;
      output: string;
      isError: boolean;
      durationMs?: number;
    }
  | { type: "finished"; reason: string }
  | { type: "notice"; text: string }
  | { type: "aborted" };

export type Incoming = (
  | ({ kind: "ready" } & Ready)
  | { kind: "agent"; event: AgentEvent }
  | {
      kind: "turn_done";
      reason: string;
      promptTokens?: number;
      completionTokens?: number;
      totalTokens?: number;
      cachedTokens?: number;
      requests?: number;
      cost?: number;
      priced?: boolean;
    }
  | Approval & { kind: "approval" }
  | { kind: "session"; path: string }
  | { kind: "session_cleared" }
  | { kind: "sessions"; sessions: SessionRow[] }
  | { kind: "loaded"; entries: LoadedEntry[] }
  | { kind: "error"; message: string }
) & { sid?: string };

export function parseAgentEvent(raw: unknown): AgentEvent | null {
  const value = asRecord(raw);
  if (!value) return null;
  switch (value.type) {
    case "turn_start":
      return { type: "turn_start" };
    case "turn_end":
      return { type: "turn_end" };
    case "assistant_text":
      return { type: "assistant_text", text: asString(value.text) };
    case "thinking":
      return { type: "thinking", text: asString(value.text) };
    case "tool_requested":
      return {
        type: "tool_requested",
        id: asString(value.id),
        name: asString(value.name),
        arguments: asString(value.arguments),
      };
    case "tool_finished":
      return {
        type: "tool_finished",
        id: asString(value.id),
        name: asString(value.name),
        output: asString(value.output),
        isError: value.isError === true,
        durationMs: asNumber(value.durationMs),
      };
    case "finished":
      return { type: "finished", reason: asString(value.reason, "ready") };
    case "notice":
      return { type: "notice", text: asString(value.text) };
    case "aborted":
      return { type: "aborted" };
    default:
      return null;
  }
}

export function parseLoadedEntry(raw: unknown): LoadedEntry | null {
  const value = asRecord(raw);
  if (!value) return null;
  const text = asString(value.text);
  switch (value.kind) {
    case "user":
    case "assistant":
    case "thinking":
      return { kind: value.kind, text };
    case "notice": {
      const level =
        value.level === "warning" || value.level === "error" ? value.level : "info";
      return { kind: "notice", level, text };
    }
    case "tool": {
      const state =
        value.state === "running" || value.state === "error" ? value.state : "ok";
      return {
        kind: "tool",
        callId: asString(value.callId),
        name: asString(value.name),
        arguments: asString(value.arguments),
        output: asString(value.output),
        state,
        durationMs: asNumber(value.durationMs),
      };
    }
    default:
      return null;
  }
}

export function parseIncoming(raw: unknown): Incoming | null {
  const value = asRecord(raw);
  if (!value) return null;
  switch (value.kind) {
    case "ready":
      return { kind: "ready", ...parseReady(value) };
    case "agent": {
      const event = parseAgentEvent(value.event);
      return event ? { kind: "agent", event } : null;
    }
    case "turn_done":
      return {
        kind: "turn_done",
        reason: asString(value.reason, "ready"),
        promptTokens: asNumber(value.promptTokens),
        completionTokens: asNumber(value.completionTokens),
        totalTokens: asNumber(value.totalTokens),
        cachedTokens: asNumber(value.cachedTokens),
        requests: asNumber(value.requests),
        cost: asNumber(value.cost),
        priced: value.priced === true,
      };
    case "approval":
      return {
        kind: "approval",
        id: asNumber(value.id) ?? 0,
        name: asString(value.name),
        arguments: asString(value.arguments),
      };
    case "session":
      return { kind: "session", path: asString(value.path) };
    case "session_cleared":
      return { kind: "session_cleared" };
    case "loaded": {
      const entries = Array.isArray(value.entries) ? value.entries : [];
      return {
        kind: "loaded",
        entries: entries
          .map(parseLoadedEntry)
          .filter((entry): entry is LoadedEntry => entry !== null),
      };
    }
    case "sessions": {
      const rows = Array.isArray(value.sessions) ? value.sessions : [];
      return {
        kind: "sessions",
        sessions: rows.map((raw) => {
          const row = asRecord(raw) ?? {};
          return {
            path: asString(row.path),
            title: asString(row.title),
            updated: asString(row.updated),
            cwd: asString(row.cwd),
          };
        }),
      };
    }
    case "error":
      return { kind: "error", message: asString(value.message, "unknown error") };
    default:
      return null;
  }
}
