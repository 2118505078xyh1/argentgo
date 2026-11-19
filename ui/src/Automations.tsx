import { For, Show, createEffect, createSignal, onCleanup, onMount } from "solid-js";
import { invoke } from "./ipc-bridge";
import { listen, type UnlistenFn } from "./ipc-bridge";

type AutomationRow = {
  id: string;
  name: string;
  schedule: string;
  kind: "prompt" | "workflow";
  action: string;
  enabled: boolean;
  nextRun: string;
  lastStatus: string | null;
  lastLog: string | null;
};

export default function Automations() {
  const [rows, setRows] = createSignal<AutomationRow[]>([]);
  const [name, setName] = createSignal("");
  const [schedule, setSchedule] = createSignal("daily 09:00");
  const [prompt, setPrompt] = createSignal("");
  const [status, setStatus] = createSignal("");
  const [error, setError] = createSignal("");
  const [log, setLog] = createSignal<{ title: string; body: string } | null>(null);
  const [preview, setPreview] = createSignal("");

  createEffect(() => {
    const value = schedule().trim();
    if (!value) {
      setPreview("");
      return;
    }
    let cancelled = false;
    onCleanup(() => {
      cancelled = true;
    });
    void invoke<string>("preview_schedule", { schedule: value })
      .then((next) => {
        if (!cancelled) setPreview(next);
      })
      .catch(() => setPreview("无效日程"));
  });

  const refresh = async () => {
    try {
      setRows(await invoke<AutomationRow[]>("list_automations"));
    } catch (err) {
      setError(String(err));
    }
  };

  onMount(async () => {
    await refresh();
    let unlisten: UnlistenFn | undefined;
    try {
      unlisten = await listen<unknown>("argent://event", (event) => {
        const value = event.payload as Record<string, unknown> | null;
        if (value?.kind !== "automation_result") return;
        setStatus(String(value.text ?? ""));
        void refresh();
      });
    } catch (err) {
      setError(String(err));
    }
    onCleanup(() => unlisten?.());
  });

  const add = async () => {
    setError("");
    try {
      await invoke("create_automation", {
        name: name(),
        schedule: schedule(),
        prompt: prompt(),
      });
      setName("");
      setPrompt("");
      setStatus("任务已保存");
      await refresh();
    } catch (err) {
      setError(String(err));
    }
  };

  const toggle = async (row: AutomationRow) => {
    try {
      await invoke("toggle_automation", { id: row.id });
      await refresh();
    } catch (err) {
      setError(String(err));
    }
  };

  const remove = async (row: AutomationRow) => {
    if (!window.confirm(`删除任务「${row.name}」？`)) return;
    try {
      await invoke("delete_automation", { id: row.id });
      await refresh();
    } catch (err) {
      setError(String(err));
    }
  };

  const run = async (row: AutomationRow) => {
    setStatus(`运行中：${row.name}`);
    try {
      await invoke("run_automation", { id: row.id });
    } catch (err) {
      setError(String(err));
    }
  };

  const showLog = async (row: AutomationRow) => {
    try {
      const body = await invoke<string>("automation_log", { id: row.id });
      setLog({ title: row.name, body });
    } catch (err) {
      setError(String(err));
    }
  };

  return (
    <section class="page">
      <header class="page-head">
        <div>
          <h2>定时任务</h2>
          <p>按日程运行 prompt，结果写入任务日志。</p>
        </div>
        <button class="ghost" onClick={() => void refresh()}>
          刷新
        </button>
      </header>

      <div class="auto-form">
        <label>
          名称
          <input value={name()} onInput={(e) => setName(e.currentTarget.value)} placeholder="每日代码审查" />
        </label>
        <label>
          日程
          <input
            value={schedule()}
            onInput={(e) => setSchedule(e.currentTarget.value)}
            placeholder="every 30m · daily 09:00 · weekly mon 09:00"
          />
          <span class="auto-preview">下次运行：{preview() || "—"}</span>
        </label>
        <label class="wide">
          提示词
          <textarea
            rows={2}
            value={prompt()}
            onInput={(e) => setPrompt(e.currentTarget.value)}
            placeholder="任务运行时发给 argent 的指令"
          />
        </label>
        <div class="auto-form-actions">
          <button class="primary" onClick={() => void add()}>
            添加任务
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
      <Show when={status()}>
        <div class="auto-status">{status()}</div>
      </Show>

      <div class="auto-list">
        <Show when={rows().length > 0} fallback={<div class="side-empty">还没有定时任务。</div>}>
          <For each={rows()}>
            {(row) => (
              <div class="auto-row" classList={{ disabled: !row.enabled }}>
                <div class="auto-main">
                  <strong>{row.name}</strong>
                  <span class="auto-meta">
                    {row.schedule} · 下次 {row.nextRun} · {row.enabled ? "已启用" : "已停用"}
                    <Show when={row.lastStatus}> · 上次 {row.lastStatus}</Show>
                  </span>
                </div>
                <div class="auto-actions">
                  <button class="ghost" onClick={() => void toggle(row)}>
                    {row.enabled ? "停用" : "启用"}
                  </button>
                  <button class="ghost" onClick={() => void run(row)}>
                    运行
                  </button>
                  <button class="ghost" disabled={!row.lastLog} onClick={() => void showLog(row)}>
                    日志
                  </button>
                  <button class="ghost" onClick={() => void remove(row)}>
                    删除
                  </button>
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
                <strong>{value().title}</strong>
                <button class="icon" onClick={() => setLog(null)}>
                  ×
                </button>
              </div>
              <pre>{value().body}</pre>
            </div>
          </div>
        )}
      </Show>
    </section>
  );
}
