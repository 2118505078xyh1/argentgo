import { For, Show, createSignal, onCleanup, onMount } from "solid-js";
import { invoke } from "./ipc-bridge";

type PtyRow = { id: string; closed: boolean; exited: boolean; elapsed: number };

export default function Terminal() {
  const [rows, setRows] = createSignal<PtyRow[]>([]);
  const [active, setActive] = createSignal("");
  const [output, setOutput] = createSignal("");
  const [command, setCommand] = createSignal("");
  const [input, setInput] = createSignal("");
  const [error, setError] = createSignal("");

  const refresh = async () => {
    try {
      setRows(await invoke<PtyRow[]>("pty_list"));
      const id = active();
      if (id) setOutput(await invoke<string>("pty_read", { id }));
    } catch (err) {
      setError(String(err));
    }
  };

  onMount(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 600);
    onCleanup(() => window.clearInterval(timer));
  });

  const spawn = async () => {
    setError("");
    try {
      const id = await invoke<string>("pty_spawn", {
        command: command(),
        cols: 100,
        rows: 24,
      });
      setActive(id);
      setCommand("");
      await refresh();
    } catch (err) {
      setError(String(err));
    }
  };

  const send = async () => {
    const id = active();
    if (!id || !input()) return;
    try {
      await invoke("pty_write", { id, text: `${input()}\n` });
      setInput("");
      await refresh();
    } catch (err) {
      setError(String(err));
    }
  };

  const kill = async (id: string) => {
    try {
      await invoke("pty_kill", { id });
      if (active() === id) setActive("");
      await refresh();
    } catch (err) {
      setError(String(err));
    }
  };

  return (
    <section class="page">
      <header class="page-head">
        <div>
          <h2>终端</h2>
          <p>持久化伪终端会话；输入会原样写入子进程。</p>
        </div>
      </header>

      <div class="auto-form">
        <label class="wide">
          启动命令
          <input
            value={command()}
            onInput={(e) => setCommand(e.currentTarget.value)}
            placeholder="powershell.exe 或 bash"
          />
        </label>
        <div class="auto-form-actions">
          <button class="primary" onClick={() => void spawn()}>
            启动终端
          </button>
        </div>
      </div>

      {error() && <div class="error-bar">{error()}</div>}

      <div class="pty-layout">
        <div class="pty-list">
          <For each={rows()} fallback={<div class="side-empty">暂无终端。</div>}>
            {(row) => (
              <button
                class="pty-tab"
                classList={{ active: row.id === active() }}
                onClick={() => setActive(row.id)}
              >
                <span>{row.id}</span>
                <span class="auto-meta">
                  {row.exited ? "已退出" : "运行中"} · {row.elapsed}s
                </span>
                <span class="icon" onClick={(e) => { e.stopPropagation(); void kill(row.id); }}>
                  ×
                </span>
              </button>
            )}
          </For>
        </div>
        <div class="pty-main">
          <pre class="pty-output">{output() || "（没有输出）"}</pre>
          <div class="pty-input">
            <input
              value={input()}
              disabled={!active()}
              onInput={(e) => setInput(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  void send();
                }
              }}
              placeholder={active() ? "输入命令…" : "先启动一个终端"}
            />
            <button class="primary" disabled={!active() || !input()} onClick={() => void send()}>
              发送
            </button>
          </div>
        </div>
      </div>
    </section>
  );
}
