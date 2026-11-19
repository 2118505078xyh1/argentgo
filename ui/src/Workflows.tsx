import { For, Show, createSignal, onMount } from "solid-js";
import { invoke } from "./ipc-bridge";

type WorkflowRow = {
  path: string;
  name: string;
  validation: { ok: boolean; steps?: number; error?: string };
};

export default function Workflows() {
  const [rows, setRows] = createSignal<WorkflowRow[]>([]);
  const [error, setError] = createSignal("");

  onMount(async () => {
    try {
      setRows(await invoke<WorkflowRow[]>("list_workflows"));
    } catch (err) {
      setError(String(err));
    }
  });

  return (
    <section class="page">
      <header class="page-head">
        <div>
          <h2>工作流</h2>
          <p>工作区中发现的工作流定义及其校验结果（只读）。</p>
        </div>
      </header>
      {error() && <div class="error-bar">{error()}</div>}
      <div class="auto-list">
        <Show when={rows().length > 0} fallback={<div class="side-empty">没有找到工作流。</div>}>
          <For each={rows()}>
            {(row) => (
              <div class="auto-row">
                <div class="auto-main">
                  <strong>{row.name}</strong>
                  <span class="auto-meta" classList={{ "error-text": !row.validation.ok }}>
                    {row.validation.ok
                      ? `${row.validation.steps} 步 · 校验通过`
                      : row.validation.error}
                  </span>
                </div>
              </div>
            )}
          </For>
        </Show>
      </div>
    </section>
  );
}
