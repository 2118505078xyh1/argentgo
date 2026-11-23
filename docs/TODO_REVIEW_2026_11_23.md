# argentgo 源码审查 TODO（2026-11-23 · 第四轮 · 源码实证）

> **生成方式**：本轮原计划 6 个只读代理分域审计，因免费额度耗尽全部中止，改由主会话直接做定向源码审查（grep 定位 → 读源码核实 → 生产调用点反查）。每条发现都附 `路径:行号`，且已与仓库现有三份清单去重：`todolist.md`（第三轮 12 条）、`docs/ARCH_AUDIT_2026_11_23.md`（5 个已修问题）、`docs/PRODUCT_ROADMAP.md`（P0/P1 大部已落地）。
>
> **基线**：HEAD `fabcdc4`（含 inbox cleanup / docs 命令等并发提交），Go 总行数 **499,709**（`git ls-files -z '*.go' | xargs -0 cat | wc -l`），距 50w 发版锚点仅差 **291 行**。
>
> **硬约束**：核心 stdlib-only、零新增 Go 模块、无 cgo、单二进制；新能力默认开启、env 仅留 `=0` 紧急回退；不做为重构而重构、不拆大函数/大文件凑行数。
>
> **优先级**：P0 = 实锤安全/数据缺口；P1 = 明显缺口或承诺未兑现；P2 = 改进与文档收尾。工作量 S/M/L。

---

## 0. 上一轮 12 条完成度核对（结论：全部已实现，清单本身过期）

逐条核对 `todolist.md` 的实现文件与生产调用点：

| # | 第三轮条目 | 实现证据 | 生产接线 | 结论 |
|---|---|---|---|---|
| 1 | grep 流式扫描 + 大小上限 | `internal/tools/search_scan.go:13-19`（4MiB 阈值 / 64KiB NUL 窗 / 64MiB 行长 / 2000 列） | `tools_builtin_search.go`、`grep_ex.go` 调用 `scanSearchFileLines` | ✅ 完成 |
| 2 | regex 容错编译 | `internal/tools/search_regex.go`（209 行，消毒 + 降级链） | `tool_search.go` / `grep_ex.go` | ✅ 完成 |
| 3 | 并行遍历 + dircache | `internal/tools/walk_parallel.go`、`repo_map_cache.go` | `grep_ex.go:48` ctx 取消接线 | ✅ 完成 |
| 4 | 截断信号 | `internal/tools/search_outcome.go` | 搜索工具统一返回 | ✅ 完成 |
| 5 | 声明式网络出口策略 | `internal/security/netpolicy.go:187 行`、`network_guard.go:109 行` | ⚠️ 部分：见 **P0-2**（生产未注入自定义策略） | ⚠️ 引擎完成，接线缺口 |
| 6 | 网络代理沙箱 | `internal/netproxy/{proxy,env,default}.go`、`bash/tools_builtin_bash.go:397-415` | 默认走 `DefaultEnv`，`=0` 关闭 | ⚠️ 完成但有并发/降级隐患，见 **P0-1** |
| 7 | RequireHuman 破窗 | `internal/security/require_human.go`、`approver.go:189-190,377` | `netproxy/proxy.go:288-289` | ✅ 完成 |
| 8 | 能力分键授权 | `internal/security/scope_path.go:147 行` | `app_execute.go:507-513`（闭包注入） | ✅ 完成 |
| 9 | always-on profiler | `internal/prof/`、`cli/doctor.go:140`、`agent/loop_helpers.go:19` 等 | 热路径 `prof.Region` + doctor 快照 | ✅ 完成 |
| 10 | token 批量计数 | `internal/tokenest/`、`agent/budget.go:346-359` | 预算路径 `EstimateItems` | ✅ 完成 |
| 11 | BM25 工具发现 | `internal/tools/toolrank/`、`tool_search.go:43-66` | schema 索引 `IndexSchema` | ✅ 完成 |
| 12 | 动态技能排序 + `$tool` | `internal/skills/selector.go:37,200,333` | `app_execute.go:608` `SetTriggerContextWithPrompt` | ✅ 完成 |

**结论**：第三轮方向选择正确且已全部落地，无需再补同类能力；当前缺口集中在「已实现能力的最后一公里接线」与「默认化收尾」。

---

## 一、P0 — 实锤安全 / 承诺缺口

### P0-1 网络代理默认实例：跨会话串味 + 启动失败静默 fail-open `[P0/M]`

- **证据**：
  - `internal/netproxy/default.go:15-44`：`defaultOnce sync.Once` + `defaultProxy/defaultEnvVal` 进程级全局；`sync.Once.Do` 闭包从**首次触发的 ctx** 取 `security.NetworkGuardFrom(ctx).Chain`（`:32-34`）和 `CallProfileFrom(ctx).ReadOnly`（`:35`），此后所有会话复用。
  - `internal/netproxy/default.go:37-42`：`New(Options{...})` 失败时直接 `return`，不设置 `defaultProxy/defaultEnvVal`，且 `sync.Once` 已标记完成 → 后续所有调用永久拿到**空 Env**；`bash/tools_builtin_bash.go:397-405` 拿到空 env 后子进程直连出网。
  - 对比：`docs/ARCH_AUDIT_2026_11_23.md` 的 P1-1 已因同类问题把 `safetyOracle` 从全局 `atomic.Value` 改为 ctx 注入；这里残留了同样形状的问题。
- **问题**：① TUI/RPC/serve 多表面并发时，A 运行的审批链/只读 profile 会被 B 运行首次触发时永久定格，后续策略变更不生效（与既有「门控读点分裂」同族）；② 代理启动失败（端口占用等）没有任何诊断，网络出口策略被静默绕过，而用户以为沙箱在保护自己。
- **建议**：
  1. `DefaultEnv` 不再用 `sync.Once` 缓存注入结果：代理本身（监听地址/transport）可进程级单例，但**审批链与只读标志必须每次从当前 ctx 读取**（`WithNetworkGuard` / `CallProfileFrom` 注入），`check()` 内按请求 ctx 取链；或把默认代理改为由 app 层每个 Execute 构造并通过 `netproxy.WithEnv` 注入。
  2. `New` 失败时返回显式错误并让 bash 出网走 fail-closed（不追加空 env 就直连），同时在诊断面输出原因。
  3. 测试：200 goroutine 并发、两条不同 deny 规则并行 Execute 验证隔离；注入 `New` 失败验证不发空 env。
- **验收**：并发下不同 run 的链各自生效；代理不可用时无静默直连；`go test -race ./internal/netproxy ./internal/bash` 干净。

### P0-2 声明式网络策略引擎无配置入口，app 层从未注入 `NetworkGuard` `[P0/M]`

- **证据**：
  - `grep` 全仓：`security.WithNetworkGuard` 的生产调用点 **0 处**（仅 `internal/security/network_guard.go:37` 定义；`internal/mcp/http.go:76` 只是从 ctx 读取）。
  - `security.NewNetPolicy` / `NetPolicyConfig{Rules: ...}` 的生产调用点 **0 处**（仅 `netpolicy.go:66,72` 自身与测试）。默认策略 `DefaultNetPolicy()` 只有内置私网/云元数据拒绝（`netpolicy.go:70-72`）。
  - 配置包无 `net` 规则块；`internal/security/network_guard.go:56-70` 的 `CheckNetworkEgress` 在无注入时退化为默认策略纯判定。
- **问题**：`todolist.md` 第 5 条承诺的「声明式策略 → 审批 → 审计」只完成了判定引擎和工具侧接线，用户**无法配置任何 deny/ask 规则**；`netproxy` 默认链为 nil， Ask 判定一律被拒（fail-closed 安全但功能不可用），deny/allow 也不进审计（`netproxy/proxy.go:303-307` 仅在 Chain 非 nil 时记审计）。
- **建议**：
  1. `internal/config` 增加 `net.rules` 配置（hostGlob/port/protocol/method/decision/requireHuman），复用 `NetPolicyConfig` 反序列化。
  2. `internal/app` 的 Execute 装配处构造 `NetPolicy` + `Chain`，经 `security.WithNetworkGuard(ctx, guard)` 注入整轮 ctx，覆盖 `web_fetch`/`web_search`/MCP HTTP-SSE/shell 子进程代理四条出口。
  3. 让 `netproxy` 在 Chain 为 nil 时也走内置审计 recorder（或明确记录「无审批通道」）。
  4. 测试：配置 deny 域后 bash `curl` 得 403 且审计有记录；allow 域免审批；`=0` 回退默认行为。
- **验收**：配置文件改一条规则即可观测到出口行为变化（端到端测试），无规则时行为逐字节不变。

### P0-3 审计日志脱敏仍是 `=1` opt-in，与不变量冲突 `[P0/S]`

- **证据**：
  - `internal/cli/optin_metadata.go:94`：`{Key: "audit_sanitize", Env: "ARGENT_AUDIT_SANITIZE", ..., Enable: "=1", Desc: "…默认关闭时只走既有 RedactJSON"}`
  - `internal/app/app_execute.go:526-529`：仅 `=1` 时设 `chain.SanitizeAudit = bash.SanitizeForLog`；`internal/security/chain.go:144` 注释明确 nil 时只走 `RedactJSON`。
  - `internal/bash/command_safety.go:248-262`：`SanitizeForLog` 专门处理命令文本中 `VAR=值`/口令状字符串。
  - `AGENTS.md:104` 不变量：「脱敏发生在任何内容写入审计日志之前」。
- **问题**：默认路径下审批 reason 里的明文口令状串只经过通用 JSON 脱敏，专门针对命令的脱敏器没上——承诺与实现有落差。这是「默认化原则」该覆盖的安全默认值，不是用户偏好。
- **建议**：默认注入 `chain.SanitizeAudit = bash.SanitizeForLog`，保留 `ARGENT_AUDIT_SANITIZE=0` 回退；补一条测试用 `password=hunter2` 的 reason 断言审计文件不含明文；同步 `optin_metadata.go` 的 `Enable` 文案与 `docs/OPT_INS.md`。
- **验收**：`go test ./internal/app -run 'Audit|Sanitize'`；`optin_docs_guard_test.go` 绿。

---

## 二、P1 — 剩余 34 个 `=1` 门控的参数归位（分批默认化）

`internal/cli/optin_metadata.go` 共 117 个独立门控：59 个已「默认开，=0 关闭」，**34 个仍为 `=1`**，3 个正整数阈值、若干路径/真值参数。按你的默认化原则（2026-11-23）分批归位；下列是逐条清单（行号为 `optin_metadata.go` 中该条目）：

### 第一批：安全 / 正确性优先（建议本轮默认开）

| 行 | Key | 说明 | 归位理由 |
|---:|---|---|---|
| 78 | `read_before_write` | 读后写新鲜度校验 | 防止基于过期快照覆盖外部改动，属写入正确性 |
| 89 | `verify_write` | 写后漂移校验 | 格式化器/concurrent edit 后给出可读告警 |
| 86 | `parse_regression_guard` | 编辑语法劣化检查 | 写完即发现语法破坏，成本可控 |
| 68 | `patch_policy_paths` | apply_patch 目标路径策略校验 | 补丁正文可能指向策略外路径，安全链闭环 |
| 72 | `block_generated_edits` | 禁止编辑生成文件 | 避免模型浪费轮次改生成物 |
| 66 | `hook_verdict` | 钩子 permissionDecision 折叠 | 钩子裁决不生效等于钩子白配 |
| 74 | `hook_rewrite` | 钩子输入/输出改写 | 同上 |
| 90 | `lsp_sanitize_env` | LSP 环境净化 | 防止继承宿主私有管道变量 |
| 91 | `lsp_kill_tree` | LSP 整树终止 | 关闭语言服务器不留孤儿孙进程 |
| 79 | `mcp_annotation_approval` | MCP 破坏性工具审批 | 声明 destructive 的工具必须审批 |
| 96 | `mcp_call_profile` | MCP 每次调用 profile | readOnlyHint 工具按每次注入只读策略 |
| 58 | `checkpoint_guard` | checkout 还原目标守卫 | 防逃逸到会话目录之外 |
| 108 | `checkpoint_workspace_diff` | 工作区差异检查点 | 与会话 checkpoint 语义配套 |
| 64 | `inspection` | 安全失败检查层总开关 | 检查链裸跑会让 Guardian 永不生效 |
| 101 | `staged_approval` | 分阶段审批 | 与审批 UX 默认化方向一致 |

### 第二批：体验 / 能力（逐个评审后默认开）

| 行 | Key | 说明 | 注意点 |
|---:|---|---|---|
| 49 | `budget_projection` | 消息预算投影 | 改变 model-visible 消息形状，需确认 golden 测试迁移 |
| 54 | `exec_session` | 统一 exec 会话（session_id/write_stdin/yield） | 注册新工具，确认 34 默认工具 golden spec 预期更新 |
| 55 | `approval_persist` | 持久化审批规则 | 新增 `<storage>/approvals.jsonl` 落盘，评估隐私影响后决定 |
| 59 | `compaction_event` | 压缩事件落盘 | 额外 JSONL 记录，评估磁盘增长 |
| 81 | `event_log` | 事件日志续传 | 同上 |
| 97 | `telemetry` | 类型化观测 | 确认 Span/Counter 无外部导出 |
| 99 | `live_steer` | 供应商原生流中重定向 | 与三协议兼容性需逐一验证 |
| 100 | `suspended_job` | Windows 挂起启动作业对象 | 仅 Windows 生效，Linux 交叉构建不受影响 |
| 123 | `persistent_shell` | 持久化 shell 会话 | 与 `exec_session` 有重叠，先确认分工再归位 |
| 124 | `builtin_shell` | 内建 shell 兜底 | **已核实是元数据漂移**：源码 `bash/tools_builtin_bash.go:284-287` 默认开、`=0` 紧急关，但 `optin_metadata.go:124` 的 `Enable` 却写 `=1`（`docs/OPT_INS.md:5` 反而写对了）。应先修元数据，不需要默认化 |
| 80 | `mcp_gateway` | MCP 单网关工具 | 改变工具表面（34→1），属「确有代价」类，需单独评审 |
| 103 | `auto_decide` | 自动决策 | 涉及无人值守行为，需评审 |
| 107 | `plan_mode` | 只规划模式 | 用户可选模式，保持 opt-in 更合适 |
| 105 | `no_intent_schema` | 关闭意图 schema | 语义是「关闭」，不要按普通开关翻转 |
| 162 | `eval_live` | 评测 live 门禁 | 会打真实 provider，保持 opt-in |

### 第三批：明确保持 opt-in（用户偏好 / 兼容性）

`vim_mode`（:37，键位偏好）、`pty_interactive`（:41，注册额外子命令）、`locale`（:22，语言偏好）、`eval_live`（:162，联网/花钱）、`plan_mode`（:107，模式切换）、`no_intent_schema`（:105，语义关闭型）。

### 实施纪律

- 每批单独提交，复用的守卫测试：`internal/app/*default_guard_test.go`、`internal/app/optin_docs_guard_test.go`（会核对 `optin_metadata.go` 的 `Enable` 文案 ↔ `docs/OPT_INS.md` ↔ 源码读点三者）。
- 默认化后保留 `=0` 紧急回退；不要把开关写进面向用户的文档当作「需要先配」。
- **前置动作**：`docs/OPT_INS.md:3` 现在仍写「一律关闭（fail-closed）」，与 `AGENTS.md:96` 默认化原则已冲突；归位时同步改掉这句话及全表「默认状态」列。

---

## 三、P1 — 大文件内存上限只覆盖了 `read` 工具，其余读点仍可 OOM `[P1/M]`

- **证据**：`internal/tools/tools_builtin_read.go:26,49-57` 定义 `readMaxBytes = 10MB` + `readFileLimited`，但全仓 `readFileLimited` 的生产调用点只有 `:116` 一处。以下无上限 `os.ReadFile`：
  - tools：`multi_edit.go:51,142`、`mutation.go:83`、`output_spill_lines.go:100`、`repo_map_cache.go:74`、`submit_plan.go:118`、`todo.go:184`、`tools_apply_patch.go:146`、`tools_builtin_write_edit.go:126,239`、`tools_context_pack.go:344`、`tools_productivity.go:56,60`、`tools_security_scan.go:135`、`write_drift.go:25`
  - bash：`coreutils.go:110`（`readFileArg` → cat/head/tail/wc 等共用）、`coreutils_grep.go:143,164`、`coreutils_archive.go:571`、`interp.go:480`
- **问题**：`todolist.md` 第 1 条只解决了搜索路径和 `read` 工具；模型对 1GB 日志跑 `cat`/`edit`/`context_pack`/`security_scan` 仍会让「文件 + 字符串副本 + 行切片」三份驻留，触发 OOM。这是同一类缺口的未覆盖部分，不是重复建设。
- **建议**：
  1. 把 `readFileLimited` 上提为 tools 包内共享 helper（保留 `errReadTooLarge` 哨兵），按用途给预算：编辑/补丁目标 ≤10MB，shell `cat` ≤10MB（附 sed/grep 提示），context_pack/security_scan ≤4MB，hash/drift 校验可用流式 hash 免读入。
  2. `bash/coreutils.go:110` 的 `readFileArg` 加同一上限；`coreutils_grep.go` 改走已实现的 `scanSearchFileLines`。
  3. 测试：创建稀疏大文件（不需真写 1GB，用 `Truncate`）断言拒绝且错误信息给出替代命令；既有 200 行输出上限测试不退化。
- **验收**：100MB 文件上 `cat`/`edit`/`grep` 峰值堆 <32MB；`go test -p 4 ./internal/tools ./internal/bash -count=1`。

> **落地记录（2026-11-23）**：已按上述建议覆盖高价值读点。tools 侧复用 `readFileLimited`，新增 `smallFileMaxBytes=4MB` 预算，接线 `context_pack`、`diff`（≤4MB）、`multi_edit`/`apply_patch`/`write`/`edit`/`todo`/`plan`（≤10MB）；`write` 对超大既有文件明确拒绝而非盲写覆盖；`mutation`、`write_drift` 改为流式 sha256（峰值内存恒定）。bash 侧新增本地未导出 `readFileLimited`（tools→bash 单向依赖，不引入新包/导出 API），接线 `readFileArg`（cat/wc/head/tail/less/diff/comm 共用）、`grep` 单文件与递归分支（≤10MB）；gzip 的**文件→文件**路径改为 `gzipFileToFile` 流式压缩/解压（stdout 路径因输出本就是字符串，仍用有界读取）。稀疏文件 `Truncate` 测试与 gzip 往返测试已补。`interp.go:480` 输入重定向刻意**未加**上限——`sort < bigfile` 是合法 shell 语义，待有明确 OOM 证据再评估。
>
> **清单修正**（代码实证）：`multi_edit.go:142` 已有 4MiB 上限（`multiEditRelocateMaxFile`）、`tools_security_scan.go:135` 已有 512KB 上限（`securityScanMaxFileBytes`），二者本就不在缺口内；`mutation.go:83`（`SnapshotMutations`）、`output_spill_lines.go:100`（`SpillRecovery`）生产零调用，优先级降为顺手改流式。

---

## 四、P2 — 已知限制收尾与文档

### P2-1 文档层面的过期与冲突 `[P2/S]`

- `todolist.md` 标题「第三轮·仅未做项」但 12 条已全部实现（见第 0 节）；应标记「已完成，归档」并在文末链接本文件，避免后续代理重复补同类能力。
- `docs/OPT_INS.md:3` 「一律关闭（fail-closed）」与 `AGENTS.md:96` 默认化原则冲突；`todolist.md:96` 提到的「参数归位」章节在该文件中并不存在（只剩「后续波次」），需补写或指向本文件第二节。
- `docs/OPT_INS.md` 的 15 键注册表仍全部标「关闭」——若沿用「注册表键是 opt-in 列表」的旧语义，应明确说明它只是历史兼容入口，实际默认由各读点决定。

### P2-2 `foveatedArchiveEnabled` 仍读进程 env `[P2/S]`

- **证据**：`internal/session/foveated_archive.go:20-24` 直接 `os.Getenv`；`session_read.go:33` `s.messages(foveatedArchiveEnabled())`；`docs/ARCH_AUDIT_2026_11_23.md:27` 已把它列为「已知限制」。
- **问题**：`--serve`/RPC/TUI 带自己的 per-run env 时，`ARGENT_FOVEATED_ARCHIVE=0` 关不掉；`MessagesForCompaction()` 虽可绕过，但调用方容易踩坑。
- **建议**：参照同轮修复的 `env func(string) string` 变体模式，加 `messagesWithFoveated(enabled bool)` 或 `MessagesFor(opts)`，在 app Execute 顶层用 `stdio.Env` 决定；保持 `Messages()` 签名不变以免大面积波及。
- **验收**：per-run env `=0` 时 `Messages()` 返回完整视图的测试。

### P2-3 新功能完成度巡检：timeline / inbox / docs URI `[P2/S-M]`

- `internal/tui/timeline/timeline.go:58-70`：`NewRecorder` 用 `os.Create` 直接落盘，每帧输出以 base64 追加；目前没看到容量上限/轮转。长时间录制可无限增长——建议加可配置 `MaxBytes` 与超限明确停止/提示，或文档注明「仅诊断短会话使用」。
- `internal/inbox/cleanup.go`：TTL/容量治理只在 `Enqueue` 时惰性触发；空闲队列的过期项不会自动清理。当前是纯库、无后台 goroutine（有意为之），可接受，但应在包文档里写明「惰性清理」语义，避免调用方误以为到点即清。
- `git log` 近期提交（`11c17be`/`d1b7842`、`63ceef7`/`04f527c`/`b4a94a0`、`a24d754`/`5f2f7c0`/`fabcdc4`）尚未进入 `docs/STATUS.md` 功能状态；按 `AGENTS.md:150` 只改 parity 状态需先跑对应测试，此处仅建议补 CHANGELOG/STATUS 条目。

### P2-4 测试与门禁的小缺口 `[P2/S]`

- `docs/ARCH_AUDIT_2026_11_23.md:31` 声称加了「AST 源码扫描穷举守卫」覆盖全部 Event kind；`grep` 未定位到对应测试文件（可能是命名不含 guard 的测试）。建议确认守卫仍存在并在 Event kind 新增时有红测。
- 本 TODO 的 P0-1/P0-2 修复后应补 `go test -race ./internal/netproxy ./internal/security ./internal/bash`（接线改动专项 race，不算慢门禁）。

---

## 五、执行顺序建议

1. **先 P0-3**（S，改动小、守住不变量）→ **P0-1**（M，消除并发串味和静默 fail-open）→ **P0-2**（M，让策略引擎真正可用）。
2. **第二批文档同步**（P2-1）与 P0 同批提交，避免守卫测试红。
3. **P1 第一批安全门控默认化**（每项单独提交，逐项 `=0` 回退验证）。
4. **P1 大文件上限**（M，独立提交，带稀疏大文件测试）。
5. **P1 第二批能力默认化**（逐个评审，尤其是会改变工具表面/输出形状的项）。
6. **P2 收尾**（foveated per-run env、timeline 上限、文档状态）。

## 六、明确不做（避免重复劳动）

- 不重做第 0 节表格里已完成的 12 条竞品优化；`todolist.md` 后续波次中「多凭据池 / 事件溯源投影 / Code Mode / SigV4 等」已在 `todolist.md:103` 登记，本文件不重复展开。
- 不拆 `runState` 82 字段、不拆 agent 主循环、不改 defer LIFO（见 `docs/ARCH_AUDIT_2026_11_23.md:41-45` 的实测结论）。
- 不把 `vim_mode`/`locale`/`plan_mode`/`eval_live` 等用户偏好或付费型能力强行默认化。
- 不为 `no_intent_schema` 这类「语义是关闭」的门控做机械翻转。
- 不引入第三方依赖、mmap、PCRE2 或 cgo。

## 验证口径

每条实现后：`gofmt -l .` 为空、`go vet ./...`、`go test -p 4 ./... -count=1`、`archguard`；接线改动按 `AGENTS.md:33` 对受影响包单独 race；不跑 `-race ./...` / Linux 交叉编译 / native 全量等发版慢门禁（留到 50w 锚点达成时一次性跑）。
