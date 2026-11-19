import { For, createSignal, onMount } from "solid-js";
import { invoke } from "./ipc-bridge";

type ExtensionList = {
  skills: string[];
  commands: string[];
  hooks: string[];
  mcpServers: string[];
  plugins: string[];
};

export default function Extensions() {
  const [data, setData] = createSignal<ExtensionList>({
    skills: [],
    commands: [],
    hooks: [],
    mcpServers: [],
    plugins: [],
  });
  const [error, setError] = createSignal("");

  onMount(async () => {
    try {
      const extensions = await invoke<Omit<ExtensionList, "mcpServers" | "plugins">>(
        "list_extensions",
      );
      const servers = await invoke<string[]>("list_mcp_servers");
      const plugins = await invoke<string[]>("list_plugins");
      setData({ ...extensions, mcpServers: servers, plugins });
    } catch (err) {
      setError(String(err));
    }
  });

  const sections: Array<{ title: string; items: () => string[]; empty: string }> = [
    { title: "Skills", items: () => data().skills, empty: "暂无 skills" },
    { title: "自定义命令", items: () => data().commands, empty: "暂无自定义命令" },
    { title: "Hooks", items: () => data().hooks, empty: "暂无 hooks" },
    { title: "MCP 服务", items: () => data().mcpServers, empty: "暂无 MCP 服务" },
    { title: "已安装插件", items: () => data().plugins, empty: "暂无插件" },
  ];

  return (
    <section class="page">
      <header class="page-head">
        <div>
          <h2>扩展</h2>
          <p>当前工作区与用户目录下发现的技能、命令和 hook 事件。</p>
        </div>
      </header>
      {error() && <div class="error-bar">{error()}</div>}
      <div class="ext-grid">
        <For each={sections}>
          {(section) => (
            <div class="ext-card">
              <h3>{section.title}</h3>
              <For
                each={section.items()}
                fallback={<div class="side-empty">{section.empty}</div>}
              >
                {(item) => <div class="ext-item">{item}</div>}
              </For>
            </div>
          )}
        </For>
      </div>
    </section>
  );
}
