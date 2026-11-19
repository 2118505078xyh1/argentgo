import { For, Show } from "solid-js";
import type { Item } from "./ipc";

export default function Plan(props: {
  items: Item[];
  status: string;
  errors: string[];
}) {
  const steps = () =>
    props.items
      .map((item, index) => ({ item, index }))
      .filter(({ item }) => item.kind === "tool" || item.kind === "notice");

  const current = () => {
    const running = props.items.find(
      (item) => item.kind === "tool" && item.tool.state === "running",
    );
    if (running && running.kind === "tool") return `正在执行：${running.tool.name}`;
    if (props.status === "thinking") return "模型生成中";
    if (props.status === "working") return "运行中";
    return "空闲";
  };

  const finalAnswer = () => {
    for (let index = props.items.length - 1; index >= 0; index -= 1) {
      const item = props.items[index];
      if (item.kind === "assistant") return item.text;
    }
    return "";
  };

  const stateLabel = (state: "running" | "ok" | "error") =>
    state === "running" ? "进行中" : state === "ok" ? "完成" : "失败";

  return (
    <section class="page">
      <header class="page-head">
        <div>
          <h2>计划与结果</h2>
          <p>由当前会话事件流派生的执行步骤、当前步骤与最终产物。</p>
        </div>
      </header>

      <div class="plan-summary">
        <div class="usage-card">
          <span>当前步骤</span>
          <strong>{current()}</strong>
        </div>
        <div class="usage-card">
          <span>已完成步骤</span>
          <strong>
            {steps().filter(({ item }) => item.kind === "tool" && item.tool.state === "ok").length}
          </strong>
        </div>
        <div class="usage-card">
          <span>状态</span>
          <strong>{props.status}</strong>
        </div>
      </div>

      <h3 class="usage-title">执行步骤</h3>
      <div class="auto-list">
        <Show when={steps().length > 0} fallback={<div class="side-empty">还没有执行步骤。</div>}>
          <For each={steps()}>
            {({ item, index }) => (
              <div class="auto-row">
                <div class="auto-main">
                  <strong>
                    {index + 1}.{" "}
                    {item.kind === "tool"
                      ? item.tool.name
                      : item.kind === "notice"
                        ? "提示"
                        : "步骤"}
                  </strong>
                  <span class="auto-meta">
                    {item.kind === "tool"
                      ? `${stateLabel(item.tool.state)} · ${item.tool.args}`
                      : item.kind === "notice"
                        ? item.text
                        : ""}
                  </span>
                </div>
              </div>
            )}
          </For>
        </Show>
      </div>

      <h3 class="usage-title">最终产物</h3>
      <div class="plan-result">
        <Show when={finalAnswer()} fallback={<div class="side-empty">尚未产生最终结果。</div>}>
          {(text) => <pre>{text()}</pre>}
        </Show>
      </div>

      <h3 class="usage-title">错误</h3>
      <div class="auto-list">
        <Show when={props.errors.length > 0} fallback={<div class="side-empty">没有错误。</div>}>
          <For each={props.errors}>
            {(message) => (
              <div class="auto-row">
                <span class="error-text">{message}</span>
              </div>
            )}
          </For>
        </Show>
      </div>
    </section>
  );
}
