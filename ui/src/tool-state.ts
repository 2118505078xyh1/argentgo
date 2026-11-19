//! Per-tab tool-card expansion state. The persisted form is deliberately flat
//! and versioned so an old or corrupt localStorage value cannot break the UI.

export type ToolExpansion = Record<string, Record<string, boolean>>;

export function toolStateKey(sid: string, toolId: string): string {
  return `${sid}\u0000${toolId}`;
}

export function readToolExpansion(raw: string | null): ToolExpansion {
  if (!raw) return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const result: ToolExpansion = {};
    for (const [sid, value] of Object.entries(parsed as Record<string, unknown>)) {
      if (!value || typeof value !== "object" || Array.isArray(value)) continue;
      const entries: Record<string, boolean> = {};
      for (const [toolId, flag] of Object.entries(value as Record<string, unknown>)) {
        if (typeof flag === "boolean") entries[toolId] = flag;
      }
      if (Object.keys(entries).length) result[sid] = entries;
    }
    return result;
  } catch {
    return {};
  }
}

export function toggleToolExpansion(
  state: ToolExpansion,
  sid: string,
  toolId: string,
): ToolExpansion {
  const current = state[sid] ?? {};
  return { ...state, [sid]: { ...current, [toolId]: !current[toolId] } };
}

export function isToolExpanded(state: ToolExpansion, sid: string, toolId: string): boolean {
  return state[sid]?.[toolId] === true;
}
