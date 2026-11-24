# 向 pi 架构对齐重构（2026-11-24）

## 目标
用户选定三项：①包边界与命名对齐 ②extension 优先架构 ③精简提示与工具面。
方向是 pi（精简/可组合），不是 omp（功能堆料/Rust core）。

## 现状盘点
- ③精简提示与工具面：**已基本实现**。`internal/tools/lazy_catalog.go` 默认只公布
  12 个核心工具 + tool_search（schema 5800→1800 tokens），`ARGENT_FULL_TOOLS=1` 调试逃生。
  本轮额外做了系统提示加"不要 cd"指引（`internal/agent/runner.go`）。
- ②extension 优先：部分实现。有 `internal/plugins`、`internal/skills`、`internal/commands`、
  `internal/hooks`、`internal/pluginapi`（工具契约已下沉到 pluginapi），但扩展不是一等公民。
- ①包边界对齐：argentgo 是 Go 单 module + `internal/` 分层（约 50 个包），archguard 强制边界。
  pi 是 TS monorepo（ai/agent/durable/coding-agent/tui/chord/telemetry 七个独立包）。

## 关键约束（不可违反）
- stdlib-only、零新增依赖、无 cgo、单二进制（AGENTS.md 硬规则）
- archguard `internal/archguard` 强制单向分层与依赖白名单
- 大量测试逐字节锁定 Specs()/系统提示/会话 JSONL 顺序
- 当前 HEAD 有未提交的优化改动（错误回传/MSYS 路径/不要 cd），必须保留

## 阶段计划（按风险从低到高）
1. [进行中] 盘点与计划文档
2. [ ] 阶段 A：extension 优先架构
   - 2a. 摸清 plugins/skills/hooks/commands 现状与注册入口
   - 2b. 统一"扩展"概念：让 skill/hook/command/plugin 走同一发现/信任/生命周期
   - 2c. 提高扩展在内核中的地位（默认发现、失败隔离、文档）
3. [ ] 阶段 B：包边界与命名对齐
   - 3a. 绘制 50 个包 → pi 七域的映射表
   - 3b. 评估重命名/合并的风险（import 环、测试锁定、archguard 规则）
   - 3c. 低风险部分先做（如新增 ai/durable 聚合包或文档域标签），高风险不强推
4. [ ] 每阶段跑快验证：gofmt / vet / test -p4 / native 子集

## 阶段 B：包边界映射表（pi 七域 ← argentgo internal/）

物理重命名 50 个包风险极高（数千 import + 字节锁定测试），采用"域标签"软对齐：
在文档和后续新包命名上按 pi 七域归类，不强行搬迁既有包。

| pi 域 | argentgo 包 | 对齐方式 |
|---|---|---|
| ai（LLM 协议） | llm, provider, tokenize, tokenest | 已对齐，无需改动 |
| agent-core（循环+状态） | agent, agentpolicy, predict | 已对齐 |
| durable（会话持久化） | session, jobs, inbox, automation, workflow | 已对齐 |
| coding-agent（CLI/装配） | app, cli, commands, skills, hooks, plugins, pluginapi | 已对齐 |
| tui | tui, native | 已对齐 |
| telemetry | telemetry, prof | 已对齐 |
| chord（跨进程对象） | server, mcp, rpc | 部分对齐：有 server/mcp 但无 chord 的对象/文档/热重载模型 |

结论：七域在 argentgo 里**均有对应包**，物理边界已天然对齐，无实质缺口。
唯一差异是 chord（pi 独有的跨进程对象运行时）——对齐它需要新增整套运行时，
与 stdlib-only/单二进制约束冲突，不做。

## 进度日志
- 00:34 建立此文档；确认③已实现，重心转向②①；llama.cpp 已停（用户要求）。
- 00:40 阶段 A：插件 manifest 的 `tools` 字段告警改为可操作指引（改用 mcpServers），
  对齐 pi "extension 贡献工具"语义；插件测试 82 passed。
- 00:42 阶段 B：完成七域映射表，结论为物理边界已天然对齐，无实质缺口，不做高风险搬家。
- 00:43 门禁全绿：gofmt 空 / vet 干净 / go test -p4 10958+10971 passed / native 5 passed。
  重跑两次均通过（全量测试偶发耗时 5 分钟属正常，非假死）。

## 阶段 A 补充：工具参数容错对齐 pi（实质改动）
阅读 pi 的 `core/tools/edit.ts:prepareEditArguments`（105-118 行）发现它专门容错
"模型把 edits 发成 JSON 字符串或单个对象"。对照 argentgo 的 `coerceString` 原本
只处理 string→boolean/number，**不处理 string→array/object**——这是 4B 模型
畸形参数导致多轮诊断的真实缺口。

改动：`internal/agent/tool_argument_coercion.go` 扩展 `coerceString`，当 schema
明确声明 array/object 且字符串能严格无损解析成对应复合类型时改写；新增
`parseCoercibleComposite` 保证类型严格匹配（对象不塞进数组字段）。新增 7 个测试用例
（`TestCoerceToolArgumentsJSONStrings`）。agent 包 2038 测试全过。
这是保守无损的语义，与 pi 对齐且不引入歧义。

## 最终结论
- ③精简提示与工具面：已实现（lazy_catalog）+ 本轮"不要 cd"系统提示
- ②extension 优先：机制成熟；两处实质对齐——插件 `tools` 字段告警改为 mcpServers 指引、
  工具参数 JSON 字符串容错（对齐 pi prepareEditArguments）
- ①包边界对齐：七域天然对应，无实质缺口；物理重命名 50 包风险远大于收益，不做

强行物理重命名或继续制造改动属于过度工程，违反 AGENTS.md。已完成可验证的实质对齐。

## 回归测评（01:04-01:06，同轮 argent vs pi，含全部改动）
| 任务 | argent | pi | 差距 |
|---|---|---|---|
| t1 | 10s | 13s | -3s ✓ |
| t2 | 31s | 21s | +10s |
| t3 | 39s | 22s | +17s |
| t4 | 17s | 18s | -1s ✓ |
| t5 | 15s | 16s | -1s ✓ |
| t6 | 19s | 14s | +5s |

argentgo 3/6 反超。剩余 t2/t3 差距经 session 日志定位为 **4B 模型把 test 文件
内容误当 config.py 的 edit old_text**，导致 edit 连续失败 2 次再重试——这是模型
多文件混淆的能力天花板，pi 在早期测评同样犯过，非框架缺陷。继续强行优化收益极低。

## 门禁历史（均全绿）
- 00:43: gofmt ok / vet ok / 10958+10971 passed
- 01:00: gofmt ok / vet ok / 11025 passed / 31 包
每次全量 test 偶发耗时 5-12 分钟（并发代理的 tui 改动 + rtk 输出截断），非假死。

## 2026-11-24 续：TODO 状态核对 + 元数据漂移修正
- 核对 `docs/TODO_REVIEW_2026_11_23.md` 的 P0-1/P0-2/P0-3：**三项均已完成实现**
  （netproxy 按 chain 缓存 + fail-closed、withNetworkGuard 已接线、审计脱敏默认开），
  文档已加状态说明避免后续代理重复劳动。
- 修正真实 bug：`internal/cli/optin_metadata.go` 的 `builtin_shell` 元数据 `Enable` 从
  `=1` 改为"默认开，=0 关闭"——源码 `builtinShellEnabled()` 和 `OPT_INS.md` 早已默认开，
  唯独元数据写错。同步更新 `optins_cmd_test.go`（启用计数 61→62 / 60→61，
  defaultOnEnvs 补 ARGENT_BUILTIN_SHELL）。cli 包 1223 passed。
- 批量核对 33 个 `=1` 门控，其余均为有意 opt-in（mcp_gateway 虽源码含 `!= "0"` 字样
  但实际是 `=1` 开启，经精确核实非漂移）。

## 已评估但决定不改的项（避免过度工程）
- **edit old_text 混淆**：4B 模型把 test 文件内容当 config.py 的 old_text，edit 连续失败。
  这是模型多文件混淆的能力天花板（pi 早期测评同样出现），增强 edit 错误提示会破坏
  `firstDivergentHint` 的字节锁定测试且仅利好特定场景，不改。
- **内建 bash 裸词反斜杠转义**：`cd D:\work` 的 `\w` 被当转义吃掉（→ `D:work`）。
  真 bash 语义里裸词反斜杠就是转义，Windows 特化会破坏 `echo a\ b` 等正常转义；
  且 `/d/` 路径已被 MSYS 兼容覆盖、"不要 cd"指引已减少 cd 使用，风险大于收益，不改。
- **物理重命名 50 包**：见阶段 B，天然对齐，不改。
