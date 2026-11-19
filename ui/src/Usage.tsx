import { For, createSignal, onMount } from "solid-js";
import { invoke } from "./ipc-bridge";
import { formatTokens } from "./format";

type ModelUsage = { name: string; turns: number; promptTokens: number; cost: number };

type Summary = {
  turns: number;
  requests: number;
  warmRequests: number;
  promptTokens: number;
  completionTokens: number;
  cachedTokens: number;
  missedTokens: number;
  cacheHitPercent: number;
  cost: number;
  models: ModelUsage[];
  daily: Array<{ day: string; turns: number; tokens: number; cost: number }>;
};

export default function Usage() {
  const [summary, setSummary] = createSignal<Summary | null>(null);
  const [error, setError] = createSignal("");

  onMount(async () => {
    try {
      setSummary(await invoke<Summary>("usage_summary"));
    } catch (err) {
      setError(String(err));
    }
  });

  const cards = () => {
    const data = summary();
    if (!data) return [];
    return [
      { label: "回合", value: String(data.turns) },
      { label: "请求", value: `${data.requests}（预热 ${data.warmRequests}）` },
      { label: "Prompt tokens", value: formatTokens(data.promptTokens) },
      { label: "Completion tokens", value: formatTokens(data.completionTokens) },
      { label: "缓存命中", value: `${data.cacheHitPercent}%` },
      { label: "未缓存", value: formatTokens(data.missedTokens) },
      { label: "累计花费", value: `$${data.cost.toFixed(4)}` },
    ];
  };

  return (
    <section class="page">
      <header class="page-head">
        <div>
          <h2>用量</h2>
          <p>来自本地 usage JSONL 的累计统计（所有会话）。</p>
        </div>
      </header>
      {error() && <div class="error-bar">{error()}</div>}
      <div class="usage-grid">
        <For each={cards()}>
          {(card) => (
            <div class="usage-card">
              <span>{card.label}</span>
              <strong>{card.value}</strong>
            </div>
          )}
        </For>
      </div>
      <h3 class="usage-title">按天</h3>
      <div class="usage-grid">
        <For
          each={summary()?.daily ?? []}
          fallback={<div class="side-empty">还没有按天记录。</div>}
        >
          {(day) => (
            <div class="usage-card">
              <span>{day.day}</span>
              <strong>
                {day.turns} 回合 · {formatTokens(day.tokens)} · ${day.cost.toFixed(4)}
              </strong>
            </div>
          )}
        </For>
      </div>
      <h3 class="usage-title">按模型</h3>
      <div class="auto-list">
        <For
          each={summary()?.models ?? []}
          fallback={<div class="side-empty">还没有记录。</div>}
        >
          {(model) => (
            <div class="auto-row">
              <div class="auto-main">
                <strong>{model.name}</strong>
                <span class="auto-meta">
                  {model.turns} 回合 · {formatTokens(model.promptTokens)} prompt · $
                  {model.cost.toFixed(4)}
                </span>
              </div>
            </div>
          )}
        </For>
      </div>
    </section>
  );
}
