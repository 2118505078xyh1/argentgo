//! Small formatting helpers shared by the UI and its unit tests.

export function formatTokens(count: number): string {
  if (count <= 0) return "—";
  if (count >= 1000) return `${(count / 1000).toFixed(1)}k`;
  return String(count);
}

export function formatCost(cost: number): string {
  if (cost <= 0) return "$0";
  if (cost < 0.0001) return "<$0.0001";
  return `$${cost.toFixed(4)}`;
}

function dayStamp(date: Date): string {
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${month}-${day}`;
}

export function sessionGroup(updated: string, today = new Date()): "今天" | "昨天" | "更早" {
  const stamp = /^(\d{2}-\d{2})/.exec(updated)?.[1];
  if (!stamp) return "更早";
  if (stamp === dayStamp(today)) return "今天";
  const yesterday = new Date(today);
  yesterday.setDate(today.getDate() - 1);
  if (stamp === dayStamp(yesterday)) return "昨天";
  return "更早";
}

export function relativeStamp(updated: string, today = new Date()): string {
  const time = /(\d{2}:\d{2})$/.exec(updated)?.[1] ?? "";
  const group = sessionGroup(updated, today);
  return group === "更早" ? updated : `${group} ${time}`.trim();
}
