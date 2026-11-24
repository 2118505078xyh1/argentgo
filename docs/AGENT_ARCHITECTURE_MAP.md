# Agent 架构框架 → argentgo 现状映射

依据外部架构评估（Observe→Think→Plan→Act→Observe 循环）整理。目标定位：
**通用 Agent Runtime（B）+ 自改进演进（D）**，Coding 是首个垂直场景。

## 七模块对照

| 模块 | 框架要求 | argentgo 现状 | 差距 / 下一步 |
|---|---|---|---|
| Model | LLM/VLM/Embedding/Reranker/专用模型 | `internal/llm` 支持 Anthropic / OpenAI Chat / Responses；多 provider 模板；能力规则 | VLM 输入有基础，Embedding/Reranker 未接；模型路由仅规则，不含成本/延迟动态路由 |
| Planning | ReAct / Plan-and-Execute / HTN / ToT | agent loop 是隐式 ReAct；`internal/workflow` 有 JSON 步骤 + journal；`child.rs` 有子代理 | **缺显式 Planner / TaskGraph**；P1 的核心缺口 |
| Memory | Working / Episodic / Semantic / Procedural | Working=`history`/`UiEvent`；Episodic=JSONL session + usage；Semantic=`internal/session` 文本事实；Procedural=`skills`/profiles | 缺向量检索 / 知识图谱；记忆搜索是字符串匹配 |
| Tool | 统一 `(Name, Description, Input, Output)` + 超时/重试/权限/校验 | `ToolRegistry` + `ToolCtx` + 策略门 + 审批 + checkpoint | 工具描述偏长（已精简）；MCP server 信任边界仍需隔离 |
| Runtime | 生命周期、调度、状态、上下文、恢复、并发、事件、权限、资源 | `internal/engine` 每会话一 worker；pause/resume/abort/steering；多标签 sid 路由 | **事件协议不统一**（CLI/TUI/native/Tauri/RPC/ACP 各自映射）→ P2 |
| Observability | 事件/日志/tracing/轨迹采集 | `UiEvent`/`AgentEvent` + usage JSONL + audit log | 缺结构化 trace/span 与轨迹采集（D 自改进的 Observe 层） |
| Efficiency | Token / 时间 / 算力成本 | 本地 4B 评测：10/12、5.9s、976 token；`cache_prompt` + 紧凑系统提示 + Git Bash 检测 | 继续降 token；多 Agent 预算分配尚未实现 |

## 核心循环现状

```
Observe  UiEvent / ToolResult / session 回放
Think    Agent loop + provider 流式
Plan     隐式（工具选择），显式 TaskGraph 缺失   ← P1
Act      ToolRegistry / bash / MCP / 子代理
Observe  工具输出 + 事件回灌                    ← 已闭环
```

循环已闭合，但 **Plan 是隐式** 的；这也是长任务成功率受限的结构性原因。

## 优先级（与 NEXT-PLAN 对齐）

1. **P0 评测**：本地 4B、Pi/OMP 对标，量化 TSR/TCA/Recovery/Token/Time。
2. **P1 Planning + Multi-Agent**：显式 TaskGraph、Orchestrator、worktree 隔离、全局预算。
3. **P2 Runtime 统一协议**：canonical events + 各传输层 adapter + conformance。
4. **P3 安全/可靠性**：OS 隔离、MCP 信任、secret 脱敏、崩溃恢复。
5. **D 自改进**：在 P0–P3 之上做 Observe/Evaluate/Optimize/Validate/Release，
   硬约束是不自动改安全策略/权限边界。

## 关键判断

- 多 Agent **不天然更强**：只有任务可分解、可并行、可独立验证时才有收益。
- 一个能稳定完成 10 个工具的 Agent，优于能调用 100 个工具但成功率低的 Agent。
- argentgo 当前最大短板不是工具数量，而是 **显式规划 + 统一协议 + 可观测性**。
- 效率优化已见成效：系统提示词从 2444 字符精简、环回缓存开启、Windows shell
  误判修复后，4B 本地评测 token 与耗时均低于此前 Pi 数据。
