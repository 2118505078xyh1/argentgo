//! Attempt badge wording. Badges are UI metadata from the bridge; they never
//! enter the agent's committed message history.

export type AttemptKind = "failed" | "retried" | "cancelled" | "stream_error";

const labels: Record<AttemptKind, { zh: string; en: string }> = {
  failed: { zh: "失败", en: "failed" },
  retried: { zh: "已重试", en: "retried" },
  cancelled: { zh: "已取消", en: "cancelled" },
  stream_error: { zh: "流式错误", en: "stream error" },
};

export function parseAttempt(value: unknown): AttemptKind | undefined {
  return typeof value === "string" && value in labels ? (value as AttemptKind) : undefined;
}

export function attemptLabel(kind: AttemptKind, locale: "zh" | "en"): string {
  return labels[kind][locale];
}
