import { For, Show, createSignal, onMount } from "solid-js";
import { invoke } from "./ipc-bridge";

export default function Git(props: { onAddToChat: (text: string) => void }) {
  const [rows, setRows] = createSignal<string[]>([]);
  const [diff, setDiff] = createSignal<{ path: string; body: string } | null>(null);
  const [error, setError] = createSignal("");

  const refresh = async () => {
    try {
      setRows(await invoke<string[]>("git_status"));
    } catch (err) {
      setError(String(err));
    }
  };

  onMount(() => void refresh());

  const open = async (line: string) => {
    const path = line.slice(3).trim();
    try {
      setDiff({ path, body: await invoke<string>("git_diff", { path }) });
    } catch (err) {
      setError(String(err));
    }
  };

  const addToChat = () => {
    const current = diff();
    if (!current) return;
    props.onAddToChat(`请检查以下 diff（${current.path}）：\n\n${current.body}`);
    setDiff(null);
  };

  return (
    <section class="page">
      <header class="page-head">
        <div>
          <h2>Git 状态</h2>
          <p>工作区改动（只读）；点击文件查看 diff 并加入对话。</p>
        </div>
        <button class="ghost" onClick={() => void refresh()}>
          刷新
        </button>
      </header>
      {error() && <div class="error-bar">{error()}</div>}
      <div class="auto-list">
        <Show when={rows().length > 0} fallback={<div class="side-empty">工作区干净。</div>}>
          <For each={rows()}>
            {(line) => (
              <button class="auto-row git-row" onClick={() => void open(line)}>
                <code>{line.slice(0, 2)}</code>
                <span class="auto-meta">{line.slice(3)}</span>
              </button>
            )}
          </For>
        </Show>
      </div>

      <Show when={diff()}>
        {(value) => (
          <div class="overlay" onClick={() => setDiff(null)}>
            <div class="log-panel" onClick={(event) => event.stopPropagation()}>
              <div class="log-head">
                <strong>{value().path}</strong>
                <div class="doc-actions">
                  <button class="primary" onClick={addToChat}>
                    加入对话
                  </button>
                  <button class="icon" onClick={() => setDiff(null)}>
                    ×
                  </button>
                </div>
              </div>
              <pre>{value().body || "（没有未暂存改动）"}</pre>
            </div>
          </div>
        )}
      </Show>
    </section>
  );
}
