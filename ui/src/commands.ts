//! Discoverable command templates and first-run quick actions. Both are data
//! only; the palette and composer decide how to apply them.

export type CommandTemplate = {
  name: string;
  hint: string;
  template: string;
  group: "turn" | "session" | "view";
};

export const COMMAND_TEMPLATES: readonly CommandTemplate[] = [
  { name: "/undo", hint: "restore files changed by the last turn", template: "/undo", group: "turn" },
  { name: "/retry", hint: "fork the last turn and re-run the prompt", template: "/retry", group: "turn" },
  { name: "/rewind", hint: "rewind n turns (default 1)", template: "/rewind $ARGUMENTS", group: "turn" },
  { name: "/name", hint: "set the generated session title", template: "/name $ARGUMENTS", group: "session" },
  { name: "/new", hint: "start a fresh session", template: "/new", group: "session" },
  { name: "/compact", hint: "compact the context window", template: "/compact", group: "session" },
  { name: "/model", hint: "switch model id", template: "/model $ARGUMENTS", group: "view" },
  { name: "/thinking", hint: "switch thinking level", template: "/thinking $ARGUMENTS", group: "view" },
  { name: "/cost", hint: "show tokens and cached usage", template: "/cost", group: "view" },
  { name: "/export", hint: "export transcript (md|json)", template: "/export $ARGUMENTS", group: "view" },
];

export function matchCommandTemplates(query: string, limit = 8): CommandTemplate[] {
  const needle = query.trim().toLowerCase().replace(/^\//, "");
  if (!needle) return COMMAND_TEMPLATES.slice(0, limit);
  return COMMAND_TEMPLATES.filter((item) =>
    `${item.name} ${item.hint}`.toLowerCase().includes(needle),
  ).slice(0, limit);
}

export type QuickAction = {
  label: { zh: string; en: string };
  prompt: string;
};

export const QUICK_ACTIONS: readonly QuickAction[] = [
  {
    label: { zh: "读懂仓库", en: "Understand the repo" },
    prompt: "Read the repository structure and summarize the main modules.",
  },
  {
    label: { zh: "找一个 bug", en: "Find a bug" },
    prompt: "Find one likely bug in this repository and explain how to fix it.",
  },
  {
    label: { zh: "解释架构", en: "Explain the architecture" },
    prompt: "Explain the architecture of this repository.",
  },
  {
    label: { zh: "写测试", en: "Write a test" },
    prompt: "Pick one untested boundary in this repository and add a focused test.",
  },
];
