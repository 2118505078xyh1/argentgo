import { For, Show, createSignal, onMount } from "solid-js";
import { invoke } from "./ipc-bridge";

type WorkspaceRow = { path: string; name: string; dir: boolean };

function readAsBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = String(reader.result);
      resolve(result.slice(result.indexOf(",") + 1));
    };
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });
}

export default function Workspace() {
  const [rows, setRows] = createSignal<WorkspaceRow[]>([]);
  const [selected, setSelected] = createSignal("");
  const [content, setContent] = createSignal("");
  const [original, setOriginal] = createSignal("");
  const [error, setError] = createSignal("");
  const [dragging, setDragging] = createSignal(false);

  const refresh = async () => {
    try {
      setRows(await invoke<WorkspaceRow[]>("list_workspace_files"));
    } catch (err) {
      setError(String(err));
    }
  };

  onMount(() => void refresh());

  const open = async (path: string) => {
    try {
      const body = await invoke<string>("read_workspace_file", { path });
      setSelected(path);
      setContent(body);
      setOriginal(body);
      setError("");
    } catch (err) {
      setError(String(err));
    }
  };

  const save = async () => {
    const path = selected();
    if (!path) return;
    try {
      await invoke("write_workspace_file", { path, content: content() });
      setOriginal(content());
    } catch (err) {
      setError(String(err));
    }
  };

  const createFile = async () => {
    const name = window.prompt("新文件路径（相对工作区）", "notes.md");
    if (!name?.trim()) return;
    try {
      await invoke("write_workspace_file", { path: name.trim(), content: "" });
      await refresh();
      await open(name.trim());
    } catch (err) {
      setError(String(err));
    }
  };

  const remove = async () => {
    const path = selected();
    if (!path || !window.confirm(`删除工作区文件「${path}」？`)) return;
    try {
      await invoke("delete_workspace_file", { path });
      setSelected("");
      setContent("");
      setOriginal("");
      await refresh();
    } catch (err) {
      setError(String(err));
    }
  };

  const upload = async (files: FileList | null) => {
    setDragging(false);
    if (!files?.length) return;
    try {
      for (const file of Array.from(files)) {
        const base64 = await readAsBase64(file);
        const path = await invoke<string>("upload_workspace_file", {
          name: file.name,
          base64Data: base64,
        });
        if (path.endsWith(".txt") || path.endsWith(".md") || path.endsWith(".json")) {
          await open(path);
        }
      }
      await refresh();
    } catch (err) {
      setError(String(err));
    }
  };

  const dirty = () => content() !== original();

  return (
    <section class="page workspace-page">
      <header class="page-head">
        <div>
          <h2>文件工作区</h2>
          <p>浏览、编辑工作区文件；拖入文件即可上传到 uploads/。</p>
        </div>
        <div class="doc-actions">
          <button class="ghost" onClick={() => void createFile()}>
            新建文件
          </button>
          <button class="ghost" onClick={() => void refresh()}>
            刷新
          </button>
        </div>
      </header>

      {error() && <div class="error-bar">{error()}</div>}

      <div class="workspace-layout">
        <div class="workspace-tree">
          <For each={rows()} fallback={<div class="side-empty">没有文件。</div>}>
            {(row) => (
              <button
                class="workspace-row"
                classList={{ active: row.path === selected(), dim: row.dir }}
                onClick={() => !row.dir && void open(row.path)}
              >
                {row.dir ? "▸ " : "· "}
                {row.name}
              </button>
            )}
          </For>
        </div>
        <div
          class="workspace-editor"
          classList={{ dragging: dragging() }}
          onDragOver={(event) => {
            event.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={(event) => {
            event.preventDefault();
            void upload(event.dataTransfer?.files ?? null);
          }}
        >
          <Show
            when={selected()}
            fallback={<div class="side-empty">选择左侧文件或拖入文件。</div>}
          >
            <div class="workspace-bar">
              <code>{selected()}</code>
              <div class="doc-actions">
                <button class="primary" disabled={!dirty()} onClick={() => void save()}>
                  保存
                </button>
                <button class="ghost" onClick={() => void remove()}>
                  删除
                </button>
              </div>
            </div>
            <textarea
              class="workspace-textarea"
              value={content()}
              onInput={(event) => setContent(event.currentTarget.value)}
            />
          </Show>
        </div>
      </div>
    </section>
  );
}
