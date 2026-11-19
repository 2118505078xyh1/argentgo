import { For, Show, createSignal, onMount } from "solid-js";
import { invoke } from "./ipc-bridge";

type MemoryRow = {
  scope: "user" | "project" | "global";
  key: string;
  value: string;
  source?: string | null;
};

const SCOPES = ["user", "project", "global"] as const;

export default function Memory() {
  const [rows, setRows] = createSignal<MemoryRow[]>([]);
  const [scope, setScope] = createSignal<string>("project");
  const [key, setKey] = createSignal("");
  const [value, setValue] = createSignal("");
  const [error, setError] = createSignal("");
  const [query, setQuery] = createSignal("");

  const refresh = async () => {
    try {
      setRows(await invoke<MemoryRow[]>("list_memory"));
    } catch (err) {
      setError(String(err));
    }
  };

  onMount(() => void refresh());

  const add = async () => {
    setError("");
    try {
      await invoke("add_memory", { scope: scope(), key: key(), value: value() });
      setKey("");
      setValue("");
      await refresh();
    } catch (err) {
      setError(String(err));
    }
  };

  const search = async () => {
    if (!query().trim()) {
      await refresh();
      return;
    }
    try {
      setRows(await invoke<MemoryRow[]>("search_memory", { query: query() }));
    } catch (err) {
      setError(String(err));
    }
  };

  const forget = async (row: MemoryRow) => {
    if (!window.confirm(`删除记忆「${row.key}」？`)) return;
    try {
      await invoke("forget_memory", { scope: row.scope, key: row.key });
      await refresh();
    } catch (err) {
      setError(String(err));
    }
  };

  return (
    <section class="page">
      <header class="page-head">
        <div>
          <h2>长期记忆</h2>
          <p>跨会话保存的事实；模型会在需要时载入。</p>
        </div>
        <div class="doc-actions">
          <input
            class="side-search"
            placeholder="搜索记忆…"
            value={query()}
            onInput={(e) => setQuery(e.currentTarget.value)}
          />
          <button class="ghost" onClick={() => void search()}>
            搜索
          </button>
          <button class="ghost" onClick={() => void refresh()}>
            刷新
          </button>
        </div>
      </header>

      <div class="auto-form">
        <label>
          范围
          <select value={scope()} onChange={(e) => setScope(e.currentTarget.value)}>
            <For each={SCOPES}>{(item) => <option value={item}>{item}</option>}</For>
          </select>
        </label>
        <label>
          键
          <input value={key()} onInput={(e) => setKey(e.currentTarget.value)} placeholder="preferred-editor" />
        </label>
        <label class="wide">
          内容
          <textarea
            rows={2}
            value={value()}
            onInput={(e) => setValue(e.currentTarget.value)}
            placeholder="记住的事实"
          />
        </label>
        <div class="auto-form-actions">
          <button class="primary" onClick={() => void add()}>
            保存记忆
          </button>
        </div>
      </div>

      <Show when={error()}>
        <div class="error-bar">
          <span>{error()}</span>
          <button class="icon" onClick={() => setError("")}>
            ×
          </button>
        </div>
      </Show>

      <div class="auto-list">
        <Show when={rows().length > 0} fallback={<div class="side-empty">还没有记忆。</div>}>
          <For each={rows()}>
            {(row) => (
              <div class="auto-row">
                <div class="auto-main">
                  <strong>
                    [{row.scope}] {row.key}
                  </strong>
                  <span class="auto-meta">{row.value}</span>
                </div>
                <div class="auto-actions">
                  <button class="ghost" onClick={() => void forget(row)}>
                    删除
                  </button>
                </div>
              </div>
            )}
          </For>
        </Show>
      </div>
    </section>
  );
}
