import { For, Show, createSignal, onMount } from "solid-js";
import { invoke } from "./ipc-bridge";

type DocumentRow = { name: string; path: string };

type DocumentView = { title: string; body: string };

export default function Documents(props: { onAddToChat: (text: string) => void }) {
  const [rows, setRows] = createSignal<DocumentRow[]>([]);
  const [document, setDocument] = createSignal<DocumentView | null>(null);
  const [error, setError] = createSignal("");

  onMount(async () => {
    try {
      setRows(await invoke<DocumentRow[]>("list_documents"));
    } catch (err) {
      setError(String(err));
    }
  });

  const open = async (row: DocumentRow) => {
    setError("");
    try {
      const body = await invoke<string>("read_document", { path: row.path });
      setDocument({ title: row.name, body });
    } catch (err) {
      setError(String(err));
    }
  };

  const addToChat = () => {
    const current = document();
    if (!current) return;
    props.onAddToChat(
      `请参考文档「${current.title}」：\n\n${current.body}`,
    );
    setDocument(null);
  };

  const copy = async () => {
    const current = document();
    if (!current) return;
    try {
      await navigator.clipboard.writeText(current.body);
    } catch (err) {
      setError(String(err));
    }
  };

  return (
    <section class="page">
      <header class="page-head">
        <div>
          <h2>文档</h2>
          <p>工作区内可提取文本的 DOCX / PPTX / XLSX / PDF（最多 100 个）。</p>
        </div>
      </header>

      <Show when={error()}>
        <div class="error-bar">
          <span>{error()}</span>
          <button class="icon" onClick={() => setError("")}>
            ×
          </button>
        </div>
      </Show>

      <div class="doc-grid">
        <Show when={rows().length > 0} fallback={<div class="side-empty">没有找到文档。</div>}>
          <For each={rows()}>
            {(row) => (
              <button class="doc-card" onClick={() => void open(row)}>
                <span class="doc-icon">▤</span>
                <span class="doc-name">{row.name}</span>
                <span class="doc-path">{row.path}</span>
              </button>
            )}
          </For>
        </Show>
      </div>

      <Show when={document()}>
        {(value) => (
          <div class="overlay" onClick={() => setDocument(null)}>
            <div class="log-panel" onClick={(event) => event.stopPropagation()}>
              <div class="log-head">
                <strong>{value().title}</strong>
                <div class="doc-actions">
                  <button class="primary" onClick={addToChat}>
                    加入对话
                  </button>
                  <button class="ghost" onClick={() => void copy()}>
                    复制
                  </button>
                  <button class="icon" onClick={() => setDocument(null)}>
                    ×
                  </button>
                </div>
              </div>
              <pre>{value().body}</pre>
            </div>
          </div>
        )}
      </Show>
    </section>
  );
}
