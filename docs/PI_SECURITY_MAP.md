# Pi 安全模型源码对照（面向 argentgo）

> 文档任务：仅核对源码与写文档，未改动任何源码。核对日期 2026-11-19。
>
> 路径缩写：
>
> - `Pi:` = `~/.pi/agent/install/releases/<version>/node_modules/@earendil-works/pi-coding-agent/dist/`
> - `argentgo:` = `本仓库根目录`
>
> 行号以核对时文件为准；Pi 的 `.js` 为编译产物，逻辑与其 `.d.ts` 一致。

---

## 1. Pi 安全机制（源码证据：文件 + 关键行为）

### 1.1 项目信任：持久 trust store + 目录作用域

Pi 的安全边界核心是**项目信任**，而不是逐命令审批。

- **存储位置**：`ProjectTrustStore` 把决策写入 `<agentDir>/trust.json`
  （`Pi: core/trust-manager.js:177-208`）。`agentDir` 默认是 `~/.pi/agent`
  （`Pi: config.js:492` 的 `CONFIG_DIR_NAME = ".pi"`；`Pi: config.js:515` 的
  `join(homedir(), CONFIG_DIR_NAME, "agent")`）。
- **决策值**：JSON 对象的值只允许 `true` / `false` / `null`
  （`Pi: core/trust-manager.js:89-99`）；`null` 表示删除该条目，用于"信任父目录"
  时清掉子目录的旧决策（`Pi: core/trust-manager.js:194-207`）。
- **目录作用域与向上查找**：`findNearestTrustEntry()` 从 cwd 逐级向上找最近的
  决策（`Pi: core/trust-manager.js:25-40`）；`getProjectTrustOptions()` 提供
  "Trust" / "Trust parent folder (…)" / "Trust (this session only)" /
  "Do not trust" / "Do not trust (this session only)" 五种选项
  （`Pi: core/trust-manager.js:41-75`）。session-only 选项 `updates: []`，不落盘。
- **并发安全**：写入用 `proper-lockfile` 加 `.lock` 文件，最多 10 次重试
  （`Pi: core/trust-manager.js:112-149`），输出按 key 排序的稳定 JSON
  （`Pi: core/trust-manager.js:101-110`）。
- **哪些资源需要信任**：`TRUST_REQUIRING_PROJECT_CONFIG_RESOURCES` 明确列出
  `settings.json`、`mcp.json`、`extensions`、`skills`、`prompts`、`themes`、
  `SYSTEM.md`、`APPEND_SYSTEM.md`（`Pi: core/trust-manager.js:8-17`）。
  `hasTrustRequiringProjectResources()` 检查 `cwd/.pi/<上述条目>`，并逐级向上
  检查 `.agents/skills`，但显式排除用户级的 `~/.agents/skills`
  （`Pi: core/trust-manager.js:150-176`）。
- **决策流程**：`resolveProjectTrusted()` 的优先级为：显式 override → 无信任资源
  则默认信任 → 扩展的 `project_trust` 事件可裁定 → 已保存决策 →
  `defaultProjectTrust`（`ask` / `always` / `never`，默认 `ask`，见
  `Pi: core/settings-defaults.js:29`）→ 无 UI 时一律返回 `false`
  （`Pi: core/project-trust.js:18-59`）。
- **加载顺序保证**：资源加载器先**强制 `projectTrusted=false`** 做一次 bootstrap，
  把项目本地扩展/包挡在外面，只加载用户/全局与临时 CLI 资源；拿到信任决策后
  才 `reload()`（`Pi: core/resource-loader.js:363-382`）。`main.js` 把该回调接到
  trust store 与 UI 上下文（`Pi: main.js:635-690`）；非交互模式在无 UI 时显示
  "project not trusted" 提示（`Pi: modes/interactive/interactive-mode.js:3568-3574`）。
- **MCP 配置同样受信任门控**：项目 `mcp.json` 只在受信任项目加载
  （`Pi: extensions/mcp/config.js:4`、`:100`）；`pi mcp` CLI 在未信任时打印
  "ignored because the project is not trusted"
  （`Pi: extensions/mcp/cli.js:167-168`、`:369`）。

### 1.2 工具名单：allow / deny + `mcp__` 命名空间

- **CLI**：`--tools/-t`（逗号分隔白名单，支持 `*`）与 `--exclude-tools/-xt`
  （黑名单），以及 `--no-tools`、`--no-builtin-tools`
  （`Pi: cli/args.js:108-132`；帮助文本 `:313-316`）。帮助明确写着
  "Keeps MCP tools unless an entry starts with `mcp__`"。
- **设置项**：`defaultTools` 支持纯名单或 `+name` / `-name` 修饰
  （`Pi: core/settings-schema.js:223-225`）；`getToolListError()` /
  `applyToolModifiers()` 做校验与增量应用
  （`Pi: core/settings-manager.js:35-73`）。
- **运行期**：`AgentSession` 用 `createToolNameMatcher()` 匹配名单/通配符；
  `_allowlistFiltersMcp` 仅在名单为空或含 `mcp__` 项时才过滤 MCP 工具
  （`Pi: core/agent-session.js:171-224`）。`_isAllowedTool()` 先判黑名单再判白名单，
  MCP 工具在白名单未显式点名时仍保留给 codemode / tool_search
  （`Pi: core/agent-session.js:1193-1214`）。
- **内置工具集**：`read / bash / powershell / edit / write / grep / find / ls`
  （`Pi: core/tools/index.js:19-28`）；只读工具集由 `createReadOnlyToolDefinitions()`
  单独构造（`Pi: core/tools/index.js:81-88`）。
- **MCP 工具命名与注解**：服务工具注册为 `mcp__<server>__<tool>`
  （`Pi: extensions/mcp/index.js:9`、`:281`）；`isMcpToolName()` 在
  `Pi: core/mcp-servers.js:36-38`。MCP 注解 `readOnlyHint` / `destructiveHint` /
  `idempotentHint` / `openWorldHint` 只作为元数据透传
  （`Pi: extensions/mcp/tools.js:176-201`），**不是审批依据**。

### 1.3 Bash：无危险分类、无逐调用审批的"裸执行"

- 参数 schema 只有 `command` 与可选 `timeout`（秒），没有审批字段
  （`Pi: core/tools/bash.js:28-31`）。
- `createLocalShellOperations()` 直接 `spawn(shell, ...)`：`cwd` 存在性检查
  （`:69-73`）、超时校验（`:16-27`）、abort/timeout 时 `killProcessTree`
  （`:96-121`）、输出累积与截断（`:196-337`）。**没有危险命令词表，也没有
  每次调用的审批门**。
- `resolveSpawnContext()` 默认把 `PI_SESSION_ID` / `PI_SESSION_FILE` /
  `PI_PROVIDER` / `PI_MODEL` / `PI_REASONING_LEVEL` 注入子进程环境
  （`Pi: core/tools/bash.js:159-181`）。
- `commandPrefix` 会在命令前拼一段配置文本（`:184`、`:197`）；
  `createLocalBashOperations()` 给扩展提供"拦截 `user_bash` 后可继续用原生执行"
  的入口（`:150-158`）——这是 Pi 里唯一能包住命令的扩展点，但它是**能力扩展**
  而非强制门禁。
- 结论：Bash 安全完全依赖"项目信任决定是否加载本地资源 + 操作者实时监督"。
  MCP 工具同样没有逐调用审批：`createMcpToolDefinition().execute()` 只是
  `client.callTool(...)`（`Pi: extensions/mcp/tools.js:203-215`）。
- 设置 schema 里没有 `denyPaths` / `denyCommands` / `permissions` 这类声明式策略块
  （`Pi: core/settings-schema.js:139-297`）；`additionalProperties: true` 只表示
  容忍未知键，不代表存在执行层策略。

---

## 2. argentgo 现有安全机制逐项对照表

| 能力 | Pi 1.5.2 | argent（源码核对） | 差距 |
|---|---|---|---|
| **项目信任存储** | `<agentDir>/trust.json`（`~/.pi/agent/trust.json`），`true/false/null`，父目录继承、session-only 选项、文件锁（`Pi: core/trust-manager.js:41-75,112-208`） | 无通用"项目信任"存储；只有 hook 文档信任库 `HookTrust`（SHA-256 摘要，`argent: internal/hooks/lib.rs:625-727`） | **大**：argentgo 没有对 cwd 的一次性信任决策与持久化作用域 |
| **项目本地资源配置门控** | `.pi/{settings,mcp,extensions,…}` 与祖先 `.agents/skills` 必须先信任；bootstrap 强制不信任（`Pi: core/trust-manager.js:8-17,150-176`；`core/resource-loader.js:363-382`） | `<cwd>/argentgo.json`、`<cwd>/.argent/config.json` 按配置优先级直接加载；`<cwd>/.argent/roles` 直接注册（`argent: internal/engine/lib.rs:1122-1129`）；hook 文档需 SHA-256 信任（`internal/pluginapi/runtime.rs:216-246`） | **大**：仓库自带的 config / roles / MCP 无需信任即可生效 |
| **MCP 项目作用域** | 项目 `mcp.json` 受项目信任门控，未信任时明确忽略（`Pi: extensions/mcp/config.js:4,100`；`extensions/mcp/cli.js:369`） | `McpConfig::load()` 无条件读取 `<cwd>/.argent/mcp.json` 与 `~/.argent/mcp.json`（`argent: internal/mcp/config.rs:148-168`） | **大**：MCP 信任边界仍需隔离（`docs/AGENT-ARCHITECTURE-MAP.md:13` 亦记录此项） |
| **工具名单过滤** | `--tools` / `--exclude-tools` + `*` 通配符 + `mcp__` 前缀特殊处理（`Pi: cli/args.js:108-132`；`core/agent-session.js:1193-1214`） | `tools.include` / `tools.exclude`（`argent: internal/core/config.rs:67,401`）、profile `tools`/`exclude_tools`（`internal/profiles/lib.rs:44,241-244`）、registry 在 `tool_search` 目录构建前先过滤（`internal/engine/lib.rs:880-881`；`internal/cli/main.rs:505-507`）；策略 `denyTools`（`security/src/lib.rs:57,157-159`） | 中：能力相当，但 argentgo 无 `mcp__` 命名空间约定；名单入口在不同层（config/profile/flag） |
| **危险命令识别** | **无**（`Pi: core/tools/bash.js:28-31,60-149`） | 三张词表 + 重定向 + 脚本解释器调用一律判危险（`POSIX_DANGEROUS` / `WINDOWS_DANGEROUS` / `PORTABLE_DANGEROUS` / `REDIRECTS` / `SCRIPT_INVOCATIONS`，`argent: internal/tools/bash.rs:12-71,167-232`） | **argentgo 明显更强**（这是刻意不向 Pi 退化的点） |
| **逐调用审批闸门** | **无**；安全依赖项目信任 + 操作者监督（`Pi: core/tools/bash.js`） | `gate_decision()` 统一顺序：策略硬门 → PreToolUse → 审批（DestructiveOnly / AllMutating）→ PostToolUse（`argent: internal/agent/tool_dispatch.rs:258-354`）；GUI 走 mpsc `Approval { reply }`（`internal/engine/lib.rs:1051-1071`） | **argentgo 明显更强** |
| **声明式硬策略** | 无对等机制（`Pi: core/settings-schema.js`） | `Policy`：`denyPaths` / `allowPaths` / `denyCommands` / `denyCommandPaths` / `denyTools` + 有序 `allow/ask/deny` 规则表（`"rules"`）（`argent: security/src/lib.rs:43-60,73-100,528-554`） | **argentgo 明显更强** |
| **路径越界防护（含符号链接）** | 无通用策略层路径检查；bash 仅检查 cwd 存在（`Pi: core/tools/bash.js:69-73`） | 策略对 lexical 与 canonical 双重匹配，无法解析的符号链接 fail-closed（`argent: security/src/lib.rs:184-224,383-416`）；`read` 拒绝工作区外绝对路径（`internal/tools/fs_tools.rs:49-72`）；bash 拒绝工作区外绝对参数（`internal/tools/bash.rs:272-300`） | **argentgo 明显更强** |
| **写入边界 / 沙箱模式** | 无；由项目信任与文件系统权限决定 | `--sandbox-mode read-only\|workspace-write\|danger-full-access`（默认 `workspace-write`）；`read-only` 摘掉 mutating 工具，`workspace-write` 用 `ToolCtx::write_roots` 限制 write/edit（`argent: internal/cli/args.rs:264-265,512-520`；`internal/cli/mainx/build.rs:82-93`；`internal/tools/fs_tools.rs:1358-1381`） | **argentgo 明显更强**（但仍是策略层，非 OS 隔离） |
| **OS 级隔离** | 无 | 无内置 OS 隔离；`--sandbox-mode` 是策略层（`docs/AGENT-ARCHITECTURE-MAP.md:35` 把 OS 隔离列为 P3） | 持平（均缺失） |
| **审计与脱敏** | 无对等审计文件；遥测为 opt-in（`Pi: core/settings-schema.js:195-203`） | `AuditLog` 按天写 `<storage>/audit/<day>.jsonl`，reason/arguments 经 `redact_text` / `redact_json` 脱敏（`argent: security/src/lib.rs:284-333`；`internal/engine/lib.rs:1098-1111`；`security/src/redact.rs:12-218`） | **argentgo 明显更强** |
| **预算 / 重复拦截自停** | 无对等机制 | `Budget` 每轮与每工具批检查；同一 `工具+理由` 被策略拦两次即 `PolicyBlocked` 结束运行（`argent: internal/agent/guards.rs:60-65`；`internal/agent/tool_dispatch.rs:64-75,247-253`） | **argentgo 明显更强** |
| **hook / 扩展信任** | 扩展受项目信任门控；无内容哈希校验（`Pi: core/project-trust.js:25-37`；`core/extensions/runner.js:109-115`） | 工作区 hook 文档必须 SHA-256 命中信任库才加载（`argent: internal/hooks/lib.rs:672-720`；`internal/pluginapi/runtime.rs:216-246`；`internal/cli/commands/extensions.rs:714-778`） | **argentgo 更强**（内容级校验）；但 argentgo 缺 Pi 的"目录级项目信任"外壳 |
| **脚本插件边界** | 扩展是受项目信任控制的 JS/TS | Rhai 引擎限制 `max_operations` / `max_expr_depths`，只注册白名单宿主函数（`argent: script-plugin/src/loader.rs:126-136,156-215`）；但 `argent_call_tool` 直接 `tool.execute(&ctx, …)`，未再走 `gate_decision`（`:188-214`） | 中：脚本内部工具调用是策略/审批旁路，需补门 |
| **会话持久化** | JSONL 会话树（非安全控制） | JSONL 为事实源，条目以 `id`/`parent` 成树，可选 SQLite 镜像（`argent: session/src/store.rs:1-35`；`session/src/tree.rs:1-3`） | 持平（均非安全边界） |

---

## 3. 结论：argentgo 比 Pi 强的地方 vs 需要向 Pi 学的地方

### 3.1 argentgo 已经明显强于 Pi

1. **执行前的判定链**：Pi 的 bash 拿到 `command` 就直接 `spawn`；argentgo 在
   `gate_decision()` 里依次跑策略硬门、PreToolUse hook、审批、PostToolUse，并把
   所有决策写进审计日志（`internal/agent/tool_dispatch.rs:263-354`）。
2. **声明式、可审查的策略**：`permissions` 块能表达路径/命令/工具级 deny 与
   `allow/ask/deny` 规则表，且先于审批——"误点审批不能放行配置里的规则"
   （`security/src/lib.rs:43-100`；`internal/engine/lib.rs:1073-1097`）。
3. **危险命令与脚本调用识别**：删除/格式化/重定向/解释器调用都会触发审批；
   审批关闭时 destructive 命令直接永久阻断（`internal/tools/bash.rs:210-232,301-319`）。
4. **路径真实解析**：符号链接目标也参与规则匹配，无法解析则 fail-closed
   （`security/src/lib.rs:192-224,383-416`）。
5. **秘密脱敏**：结构化 JSON 按 key 归一化脱敏，文本按前缀/形状脱敏，覆盖
   `sk-` / `ghp_` / `AKIA` / JWT / Bearer / `api_key=` 等
   （`security/src/redact.rs:37-218`）。
6. **自停机制**：policy 连续两次拦截同一效果即终止运行，避免模型绕路穷举
   （`internal/agent/guards.rs:60-89`）。

### 3.2 需要向 Pi 学的地方（项目信任 + 资源作用域）

Pi 的安全哲学不是"拦住每条危险命令"，而是"**先决定这个目录的代码值不值得执行**"。
argentgo 目前缺的正是这一层外壳：

1. **一次性、持久化的项目信任决策**。Pi 在存在项目本地资源时弹窗，记住
   `true/false`（或父目录 / 仅本次），无 UI 时默认拒绝
   （`Pi: core/project-trust.js:42-58`）。argentgo 目前对仓库自带配置没有对应门。
2. **"先按不信任加载、拿到决策再重载"的启动顺序**。Pi 的 bootstrap 强制
   `projectTrusted=false`（`Pi: core/resource-loader.js:363-382`），确保恶意仓库
   不能在信任决策前注入扩展/config；argentgo 的 `<cwd>/argentgo.json`、
   `.argent/roles`、`.argent/mcp.json` 都在决策前生效。
3. **资源作用域清单**。Pi 用显式清单界定"哪些项目文件属于可执行/可加载资源"
   （`Pi: core/trust-manager.js:8-17`），并显式排除用户级 `~/.agents/skills`。
   argentgo 的 hook 已有内容级信任（`HookTrust`），但缺"目录级作用域 + 用户资源
   与项目资源区分"这一层；且没有父目录继承与 session-only 选项。
4. **MCP 项目级信任**。Pi 的 `mcp.json` 受项目信任门控；argentgo 直接合并项目
   MCP 配置（`internal/mcp/config.rs:152-168`），一个未审查仓库可以声明自己的
   MCP server。

> 注意：学习 Pi 的"项目信任"是**增加一层门**，不是把 argentgo 现有的
> 策略/审批/审计/危险检测替换掉。两者是叠加关系。

---

## 4. P3 落地清单（按优先级，带文件落点）

对齐现有优先级定义：`P3 安全/可靠性：OS 隔离、MCP 信任、secret 脱敏、崩溃恢复`
（`docs/AGENT-ARCHITECTURE-MAP.md:35`）。以下按优先级排序。

### P3-1（最高）项目信任外壳 + 资源作用域

- 新增 `security/src/project_trust.rs`：仿 `ProjectTrustStore` 的
  `true/false/null` 存储、cwd 向上查找、父目录继承、session-only 选项、
  路径规范化与文件锁；存储落点 `<storage>/project-trust.json`（避免与 hook
  信任库混淆）。
- 新增资源作用域常量（对应 Pi 的 `TRUST_REQUIRING_PROJECT_CONFIG_RESOURCES`）：
  `argentgo.json` / `.argent/{config.json,mcp.json,extensions,skills,prompts,themes,roles,hooks}` /
  祖先 `.agents/skills`（排除 `~/.agents/skills`）。
- `internal/cli/main.rs` 与 `internal/engine/lib.rs`：在**加载 project config /
  roles / hooks / MCP 之前**先做"不信任 bootstrap → 解析信任 → reload"的两阶段
  加载；无 UI（print/rpc/serve）时默认 `false` 或要求显式 `--trust-project`。
- TUI 增加 `/trust` 选择面板与状态提示（落点 `internal/cli/tui/app.rs`、
  `internal/cli/tui/render.rs`；GUI 对应 `internal/engine/lib.rs` 的
  `UiEvent` 扩展）。
- 边界测试落点：`security/src/project_trust.rs` 内联 `#[cfg(test)]`，
  以及 `internal/cli/tests/` 里"未信任仓库的 config/role/mcp 不生效"进程级测试。

### P3-2 MCP 项目信任与作用域（对齐 Pi）

- `internal/mcp/config.rs:148-168`：项目 `mcp.json` 仅在 P3-1 判定受信任时合并；
  未信任时给出与 Pi 类似的明确提示（"ignored because the project is not trusted"）。
- `internal/mcp/lib.rs`：server 配置增加 `scope: global | project` 与 `trusted`
  标记；`internal/engine/lib.rs` 注册 MCP 工具时按信任过滤（对应 Pi 的
  `mcp__` 命名空间过滤约定），并保留 `mcp__<server>__<tool>` 命名以便名单规则复用。
- 测试：`internal/mcp/config.rs` 增"untrusted project mcp.json is skipped"。

### P3-3 脚本插件宿主调用回归门禁

- `script-plugin/src/loader.rs:188-214`：`argent_call_tool` 在
  `tool.execute()` 之前调用与 agent loop 相同的 policy/approval/audit 门；
  可通过向 `LoadedScript` 注入一个 `PolicyApproval` + `AuditHook` 句柄实现，
  避免在 Rhai runtime 内复制判断逻辑。
- 测试：`script-plugin/tests/` 增"脚本内调用已 deny 的工具被拒绝"。

### P3-4 secret 脱敏补强

- `security/src/redact.rs:37-112`：补 Pi/常见生态前缀（如 `sk-proj-`、
  `xoxa-`、通用 `eyJ...` 边界校验）与 `key: value` 中带引号/换行的形态；
  增加对 tool output 与 provider 请求方向的调用点核查（落点 `internal/engine/lib.rs`、
  `internal/llm/*`），确保两条出口都调用 `redact_*`。

### P3-5 OS 隔离与沙箱边界（不退化）

- `internal/cli/args.rs:264-265`、`internal/cli/mainx/build.rs:82-93` 保持
  `--sandbox-mode` 三档语义；文档明确它是**策略层而非 OS 隔离**。
- 为容器/沙箱运行补充 `docker/Dockerfile` 与文档示例（`docs/ARCHITECTURE.md`、
  `README.md`），不引入浏览器/Node 运行时，保持单二进制约束。

### P3-6 崩溃恢复与审计完整性

- `session/src/store.rs:12-22` 的 `FlushPolicy::EveryAppend` 保持默认；
  补"进程异常退出后 JSONL 可重放"的测试。
- `security/src/lib.rs:284-333` 的审计写入已是 append + flush；
  补充审计目录权限与轮转策略说明（落点 `docs/ARCHITECTURE.md` 的策略章节）。

---

## 5. 明确不向 Pi 的"裸执行"退化

以下能力是 argentgo 相对 Pi 的核心安全增量，**任何向 Pi 对齐的改动都不得移除或绕过**：

1. **不移除 bash 危险命令分类与审批门**
   （`internal/tools/bash.rs:12-232,267-319`）。Pi 的 bash 拿到字符串即执行
   （`Pi: core/tools/bash.js:60-149`），argentgo 不采用该模型。
2. **不移除 `Policy` 硬门禁及其"先于审批"的顺序**
   （`security/src/lib.rs:156-182`；`internal/agent/tool_dispatch.rs:263-299`）。
3. **不移除工作区/绝对路径边界检查与符号链接 fail-closed**
   （`security/src/lib.rs:184-224`；`internal/tools/fs_tools.rs:49-72`；
   `internal/tools/bash.rs:272-300`；`tauri-app/src/backend.rs:893-919`）。
4. **不移除审计日志与脱敏**（`security/src/lib.rs:284-333`、
   `security/src/redact.rs`）。
5. **不移除 hook 文档的 SHA-256 信任校验**
   （`internal/pluginapi/runtime.rs:216-246`）；新增项目信任是**叠加**，不是替代。
6. **不移除"两次策略拦截即终止"的自停机制**
   （`internal/agent/guards.rs:60-65`）。
7. **不为"参数一致"引入 Pi 式 `user_bash` 任意改写钩子**
   （`Pi: core/tools/bash.js:150-158,186`）：若增加扩展拦截点，必须保持
   "拦截后仍回到 argentgo 的 gate_decision" 的不变量。

---

## 6. P3 实施状态（2026-11-19）

已落地：

- **P3-1 项目信任**：`security/src/trust.rs` 的 `TrustStore` 持久化
  cwd/父目录决策；CLI `--trust-project` / `--no-trust-project` 与
  `ARGENT_PROJECT_TRUSTED`；CLI 和 engine/Tauri 均在读取任何 cwd 相对配置
  之前决定信任，未信任时使用中性 discovery 根。角色与斜杠命令发现也已纳入。
- **P3-2 脚本插件旁路**：`script-plugin/src/loader.rs` 的
  `argent_call_tool` 增加 ToolCtx 级 gate，阻止审批关闭时执行破坏性命令、
  阻止越过 write_roots 的路径调用。
- **审批关闭时的硬拒绝**：`bash` 在 `approval=never` 下拒绝危险命令，
  且错误信息不再提示环境变量绕过方式。

进行中：MCP 每服务器权限隔离（P3-3），由独立子代理处理。

未开始：secret 脱敏范围复核（P3-4）、OS 级隔离（P3-5）、
崩溃恢复与审计轮转（P3-6）。
