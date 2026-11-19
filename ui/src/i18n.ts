//! Minimal UI dictionary for the prototype. The Rust side keeps its own
//! embedded i18n; this file only covers frontend chrome.

export type Locale = "zh" | "en";

const zh = {
  chat: "聊天",
  automations: "定时任务",
  documents: "文档",
  newTask: "新任务",
  searchSessions: "搜索会话…",
  ready: "就绪",
  running: "运行中",
  model: "模型",
  thinking: "思考",
  rewind: "撤销",
  help: "帮助",
  stop: "停止",
  send: "发送",
  steer: "插话",
  allow: "允许",
  alwaysAllow: "总是允许",
  deny: "拒绝",
  approvalTitle: "允许工具调用？",
  newTab: "新建会话标签",
  copy: "复制",
  retry: "重试",
  composerHint: "Enter 发送 · Shift+Enter 换行",
  steerHint: "Enter 插话 · 将在工具边界注入 · Shift+Enter 换行",
  composerPlaceholder: "给 argent 发消息…",
  steerPlaceholder: "运行中，输入内容会插话…",
  welcome: "问点什么，或者输入 / 看命令。",
  memory: "记忆",
  extensions: "扩展",
  usage: "用量",
  git: "Git",
} as const;

type Key = keyof typeof zh;

const en: Record<Key, string> = {
  chat: "Chat",
  automations: "Automations",
  documents: "Documents",
  newTask: "New task",
  searchSessions: "Search sessions…",
  ready: "Ready",
  running: "Running",
  model: "Model",
  thinking: "Thinking",
  rewind: "Rewind",
  help: "Help",
  stop: "Stop",
  send: "Send",
  steer: "Steer",
  allow: "Allow",
  alwaysAllow: "Always allow",
  deny: "Deny",
  approvalTitle: "Allow tool call?",
  newTab: "New session tab",
  copy: "Copy",
  retry: "Retry",
  composerHint: "Enter to send · Shift+Enter for newline",
  steerHint: "Enter to steer · Shift+Enter for newline",
  composerPlaceholder: "Message argent…",
  steerPlaceholder: "Running — typing here steers the turn…",
  welcome: "Ask something, or type / for commands.",
  memory: "Memory",
  extensions: "Extensions",
  usage: "Usage",
  git: "Git",
};

export function detectLocale(): Locale {
  try {
    const saved = localStorage.getItem("argent.locale");
    if (saved === "zh" || saved === "en") return saved;
  } catch {
    // Storage may be unavailable; fall back to the browser locale.
  }
  return navigator.language.toLowerCase().startsWith("zh") ? "zh" : "en";
}

export function t(locale: Locale, key: Key): string {
  return locale === "zh" ? zh[key] : en[key];
}
