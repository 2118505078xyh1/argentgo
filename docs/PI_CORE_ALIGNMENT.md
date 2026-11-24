# Pi 1.5.2 内核对照（源码级）

来源：`%USERPROFILE%\.pi\agent\install\releases\<version>\node_modules\@earendil-works\pi-coding-agent\dist`

## Pi 的真实内核边界

`core/tools/index.js` 明确列出 8 个内置工具：

```
read, bash, powershell, edit, write, grep, find, ls
```

但 `createCodingToolDefinitions()` 默认只返回 **4 个**：

```
read, bash, edit, write
```

`grep / find / ls / powershell` 通过 settings 的 `defaultTools` 开启。
系统提示词 `core/system-prompt.js` 由：

- 一句话身份（默认 "You are a coding assistant running inside pi..."）
- 每个当前工具一行 snippet
- 根据当前工具动态生成的 rules（如 `Use bash for file operations like ls, rg, find`）
- cwd + 文档说明

拼成，**没有**长篇行为训诫。

Pi 的 `extensions/` 只有 4 类：`codemode`、`llama`、`mcp`、`tool-search`；
subagents、web access、skills、themes 等都在扩展/包里，不在内核工具表。

## argentgo 对照

| Pi | argentgo | 位置 |
|---|---|---|
| 核心 4 工具 | `read / write / edit / bash` | `internal/tools/plugin.rs::core_tools_with_mode()` |
| `grep / find / ls` 可选核心 | `explore` 插件，默认加载 | `plugin.rs::EXPLORE` + `DEFAULT_PLUGINS` |
| `powershell` | 合入 `bash` 的 Windows shell 检测 | `internal/tools/bash.rs::ShellFlavor::detect()` |
| 系统提示词 | 已改为同样的紧凑结构 | `internal/agent/loop_fns.rs::build_system_prompt()` |
| MCP | 扩展 | `internal/mcp` + CLI 动态加载 |
| subagents | 扩展/插件 | `internal/tools/delegate.rs` + `delegate` 插件 |
| skills | 扩展 | `internal/tools/skills.rs` |
| web | 插件 | `plugin.rs::WEB` |
| plan/todo | 插件 | `plugin.rs::PLAN` |
| memory | 插件 | `plugin.rs::MEMORY` |
| office | 插件 | `plugin.rs::OFFICE` |

**结论**：argentgo 的内核已经是 Pi 式 4 工具，其余能力都在插件/扩展层。
后续优化原则：

1. `ToolRegistry::with_core()` 只允许 4 个工具，任何新能力必须声明为插件；
2. 默认插件只允许 `explore`（对应 Pi 的 grep/find/ls）；
3. 系统提示词保持"身份 + 工具行 + 少量 rules"，不得再加行为训诫长段；
4. 对标 Pi 的新功能时，先判断它属于内核（4 工具之一）还是插件。

## 回归保护

`internal/tools/registry.rs` 增加测试：

- `core_registry_has_exactly_pi_default_tools`：核心工具表必须恰好是
  `read/write/edit/bash`；
- `default_plugins_only_explore`：默认插件表只允许 `explore`。

这两条测试防止功能被重新塞回内核。
