import { For, Show } from "solid-js";

export type Tool = {
  id: string;
  name: string;
  args: string;
  state: "running" | "ok" | "error";
  output: string;
  duration?: number;
};

export default function ToolCard(props: {
  tool: Tool;
  expanded: boolean;
  onToggle: () => void;
}) {
  const stateLabel = () => {
    if (props.tool.state === "running") return "运行中";
    if (props.tool.state === "error") return "失败";
    return "完成";
  };
  return (
    <div
      class="tool"
      classList={{
        failed: props.tool.state === "error",
        running: props.tool.state === "running",
        expanded: props.expanded,
      }}
    >
      <button class="tool-head" onClick={props.onToggle}>
        <span class="tool-state">
          {props.tool.state === "error" ? "✗" : props.tool.state === "running" ? "·" : "✓"}
        </span>
        <code class="tool-name">{props.tool.name}</code>
        <span class="tool-args">{props.tool.args}</span>
        <span class="spacer" />
        <span class="tool-meta">
          {stateLabel()}
          {props.tool.duration !== undefined ? ` · ${props.tool.duration}ms` : ""}
        </span>
        <span class="tool-toggle">{props.expanded ? "▾" : "▸"}</span>
      </button>
      <Show when={props.expanded}>
        <div class="tool-detail">
          <Show when={props.tool.args}>
            <div class="tool-detail-block">
              <span class="tool-detail-label">原始参数</span>
              <pre>{props.tool.args}</pre>
            </div>
          </Show>
          <Show when={props.tool.output}>
            <div class="tool-detail-block">
              <span class="tool-detail-label">输出</span>
              <pre class="tool-output">
                <For each={props.tool.output.split("\n")}>
                  {(line) => (
                    <div
                      classList={{
                        "diff-add": line.startsWith("+") && !line.startsWith("+++"),
                        "diff-del": line.startsWith("-") && !line.startsWith("---"),
                      }}
                    >
                      {line}
                    </div>
                  )}
                </For>
              </pre>
            </div>
          </Show>
        </div>
      </Show>
    </div>
  );
}
