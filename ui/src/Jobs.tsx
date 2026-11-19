import { For, Show, createSignal, onMount } from "solid-js";
import { invoke } from "./ipc-bridge";

type JobRow = {
  id: string;
  command: string;
  status: "running" | "succeeded" | "failed" | "killed";
  exitCode: number | null;
  elapsed: number;
};

export default function Jobs() {
  const [rows, setRows] = createSignal<JobRow[]>([]);
  const [command, setCommand] = createSignal("");
  const [log, setLog] = createSignal<{ id: string; body: string } | null>(null);
  const [error, setError] = createSignal("");

  const refresh = async () => {
    try {
      setRows(await invoke<JobRow[]>("list_jobs"));
    } catch (err) {
      setError(String(err));
    }
  };

  onMount(() => void refresh());

  const start = async () => {
    setError("");
    try {
      await invoke<string>("start_job", { command: command() });
      setCommand("");
      await refresh();
    } catch (err) {
      setError(String(err));
    }
  };

  const tail = async (row: JobRow) => {
    try {
      setLog({ id: row.id, body: await invoke<string>("job_tail", { id: row.id, lines: 200 }) });
    } catch (err) {
      setError(String(err));
    }
  };

  const kill = async (row: JobRow) => {
    try {
      await invoke("kill_job", { id: row.id });
      await refresh();
    } catch (err) {
      setError(String(err));
    }
  };

  return (
    <section class="page">
      <header class="page-head">
        <div>
          <h2>后台任务</h2>
          <p>独立于回合运行的命令；日志按任务保存。</p>
        </div>
        <button class="ghost" onClick={() => void refresh()}>
          刷新
        </button>
      </header>

      <div class="auto-form">
        <label class="wide">
          命令
          <input
            value={command()}
            onInput={(e) => setCommand(e.currentTarget.value)}
            placeholder="cargo test --workspace"
          />
        </label>
        <div class="auto-form-actions">
          <button class="primary" onClick={() => void start()}>
            启动任务
          </button>
        </div>
      </div>

      {error() && <div class="error-bar">{error()}</div>}

      <div class="auto-list">
        <Show when={rows().length > 0} fallback={<div class="side-empty">暂无后台任务。</div>}>
          <For each={rows()}>
            {(row) => (
              <div class="auto-row">
                <div class="auto-main">
                  <strong>{row.command}</strong>
                  <span class="auto-meta">
                    {row.id} · {row.status} · {row.elapsed}s
                    {row.exitCode !== null ? ` · exit ${row.exitCode}` : ""}
                  </span>
                </div>
                <div class="auto-actions">
                  <button class="ghost" onClick={() => void tail(row)}>
                    日志
                  </button>
                  <Show when={row.status === "running"}>
                    <button class="ghost" onClick={() => void kill(row)}>
                      终止
                    </button>
                  </Show>
                </div>
              </div>
            )}
          </For>
        </Show>
      </div>

      <Show when={log()}>
        {(value) => (
          <div class="overlay" onClick={() => setLog(null)}>
            <div class="log-panel" onClick={(event) => event.stopPropagation()}>
              <div class="log-head">
                <strong>{value().id}</strong>
                <button class="icon" onClick={() => setLog(null)}>
                  ×
                </button>
              </div>
              <pre>{value().body || "（暂无日志）"}</pre>
            </div>
          </div>
        )}
      </Show>
    </section>
  );
}
