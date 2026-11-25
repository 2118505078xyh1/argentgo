# 编码 Agent / Harness 功能清单（源码实证版）

> 日期：2026-11-24。方法：三路并行只读调研——竞品功能全集（codex / cline / opencode /
> pi-mono / oh-my-pi / dsh / OpenHands / kilocode / crush / killi）、argentgo 自身基线、
> 跨竞品架构级能力范式。所有条目均可追溯到源码/文档证据。
>
> **用途**：这是"一个生产级编码 agent/harness 应该具备什么"的详尽功能清单，不是待办列表。
> 每条标注 argentgo 当前状态，便于对标与规划。
>
> **状态标注**：
> - `[已有]` 生产路径已接线 · `[默认开]` 默认启用、`=0` 可回退 · `[opt-in]` 默认关需显式开
> - `[部分]` 有原语/测试但未接生产路径 · `[缺口]` 尚未实现 · `[反模式]` 竞品明确否定、不应做
>
> **四大数据底座**（跨所有竞品收敛的共识）：A) 事件溯源+投影的单一事实源；B) 无状态可恢复
> 执行循环 + WAL claim；C) 能力接缝三角色 + 可逆注册；D) 分层配置 + 信任门 + 安全链。

---

## 1. 工具系统（Tools）

### 1.1 文件与代码操作
1. **整文件读**（带尺寸预检 + 替代命令提示）— `[已有]` read + readFileLimited（10MB）
2. **精确编辑**（唯一匹配校验 + 锚点/哈希行定位）— `[已有]` edit / multi_edit
3. **结构化补丁**（apply_patch 语义）— `[opt-in]` ARGENT_OPT_INS=apply_patch
4. **AST 结构化编辑**（只读 diff 预览、不写盘）— `[默认开]` ast_edit
5. **批量写 + 工作区边界** — `[已有]` write
6. **目录树/符号图**（repo_map）— `[已有]` repo_map / codegraph_query(opt-in)
7. **worktree 三件套**（自建隔离工作区）— `[默认开]` worktree_create/list/remove
8. **文件落盘限流/折叠/大输出落盘** — `[已有]` output_collapse / output_spill

### 1.2 搜索与发现
9. **grep 流式扫描 + 大小上限 + 二进制前置检测** — `[已有]` grep + grep_ex(opt-in)
10. **regex 容错降级链**（消毒/括号重试/字面量兜底）— `[已有]`
11. **并行目录遍历 + 进程级 dircache**（stamp 失效）— `[已有]`
12. **截断信号 + 超时建议** — `[已有]`
13. **glob 模式匹配** — `[已有]`
14. **BM25 工具发现**（按需披露工具、压缩 schema）— `[已有]` tool_search
15. **动态技能排序 + $tool 提及路由** — `[已有]`

### 1.3 Shell 与进程
16. **纯标准库自研 shell**（lexer/parser/interp/coreutils）— `[已有]` bash，零依赖
17. **PowerShell 工具**（Windows）— `[已有]`
18. **持久化 shell 会话**（复用 cd/venv 状态）— `[opt-in]` persistent_shell
19. **exec session / write_stdin**（长驻会话续读）— `[opt-in]` exec_session
20. **进程硬化**（有界捕获/整树终止/环境净化/UTF-8 重组）— `[默认开]` bash_use_proc
21. **命令风险静态分级**（forbidden/destructive）— `[已有]` shell risk grading
22. **PTY / ConPTY 交互终端** — `[已有]` pty 包 + `argentgo pty`
23. **后台作业注册表 + cron 调度** — `[已有]` jobs + automation

### 1.4 语言服务与调试
24. **LSP 客户端**（定义/引用/符号/重命名/格式化/inlay hint/code lens 等 15 方法）— `[已有]` 6 默认工具 + 进阶
25. **DAP 调试工具 8 个**（start/breakpoint/continue/step/threads/stack/variables/stop）— `[默认开]` ARGENT_DAP
26. **AST 解析缓存** — `[已有]` astcache
27. **代码图谱索引/查询** — `[opt-in]` codegraph

### 1.5 Web / 浏览器 / 桌面
28. **web_fetch（私网/云元数据拒绝 + 内容清洗）** — `[已有]`
29. **web_search（预设 Brave/Tavily/SearXNG）** — `[部分]` 无内置后端，需用户 key
30. **Chrome CDP 浏览器工具**（open/snapshot + web_fastfetch）— `[默认开]` ARGENT_BROWSER
31. **Windows 桌面控制 computer**（截屏+坐标输入、mutating 走审批）— `[默认开]` ARGENT_COMPUTER

### 1.6 任务 / 规划 / 记忆 / 元工具
32. **task 子代理委派**（按 id 续跑、worktree 隔离、后台队列、成本对账）— `[已有]`
33. **todo 任务列表** — `[已有]`
34. **submit_plan / exit_plan 计划审批闭环** — `[已有]` / `[opt-in]` plan_mode
35. **跨会话记忆库**（retain/recall/forget/reflect/learn/consolidate）— `[已有]`
36. **memory 规则发现 + 召回排序** — `[默认开]` rule_discovery
37. **ask_user / request_secret 交互工具**（TTY 主动提问/索取凭据）— `[已有]`
38. **tool_search 元工具** — `[已有]`
39. **models / switch_model 模型自驱切换** — `[默认开]`
40. **checkpoint_diff / history / usage / read_output_artifact** — `[已有]`
41. **MCP 工具动态挂载**（server__tool 命名空间）— `[已有]`；resources/templates + `list_mcp_resources`/`read_mcp_resource` 已接线
42. **MCP 单网关**（整表收敛为稳定 spec）— `[opt-in]` mcp_gateway
43. **hook daemon 常驻扩展进程**（SHA-256 信任门）— `[opt-in]` hook_daemon

---

## 2. Agent 循环与执行（Loop）

44. **流式输出 → 工具 → 追加 JSONL 会话，循环往复** — `[已有]`（核心范式）
45. **无状态循环：每步从重载历史继续，禁止内存内 tool loop** — `[已有]`（竞品共识，反模式防护）
46. **预算/轮次/max turns 停止** — `[已有]`
47. **自动压缩 + 重试循环** — `[已有]`（background_compact 默认开）
48. **历史裁剪/去重/steering（用户中途重定向）** — `[已有]`
49. **follow-up / steering 双队列** — `[已有]`
50. **子代理并行 + 共享父 system 前缀 + worktree 隔离** — `[已有]`
51. **重试/失败分类/降级链**（provider 重试 + 错误分类）— `[已有]`
52. **取消 context 一轮内停止运行** — `[已有]`
53. **工具输出不在 UTF-8 字符中间截断** — `[已有]`
54. **循环检测/伪进展拦截**（去重 + 升级）— `[默认开]` loop_detection
55. **工具错误反射**（把失败原因回灌模型）— `[默认开]` tool_error_reflection
56. **工具错误输出并入 stderr/stdout 原文** — `[默认开]` tool_error_output
57. **plan mode 只规划不改文件** — `[opt-in]` plan_mode
58. **live steering（流中重定向）** — `[opt-in]` live_steer
59. **推测执行 / 预热 / 后台 kernel** — `[缺口]`（omp 有，与单二进制约束冲突）
60. **write-ahead execution claim + 崩溃恢复** — `[部分]` turn lease + claims，非完整 claim 语义
61. **backpressure（慢消费者折叠更新）** — `[已有]` EventHub 满缓冲订阅者淘汰 + 终态预留槽
62. **inbox 投递与执行分离 + 幂等 ID** — `[部分]` internal/inbox 存在但未与执行闭环
63. **Code Mode DSL（受约束脚本调工具）** — `[已有]` 顺序/parallel 分波执行 + dependsOn + return 过滤，每步走审批链
64. **goal 目标循环 / 有界迭代** — `[已有]` /goal

---

## 3. 上下文管理（Context）

65. **结构化压缩**（保留改动文件/失败/下一步）— `[已有]`
66. **模型生成式压缩摘要** — `[opt-in]` model_summary + secondary-model
67. **foveated 多保真归档**（两端逐字、中段稀疏 + gap 标记）— `[默认开]`
68. **上下文预算预测 / 压力监测** — `[默认开]` budget_projection / context_calibration
69. **token 精确计数 + 批量并行 + CJK 估算** — `[已有]`
70. **提示缓存可见性** — `[部分]` wire 已支持；长生命周期 runner 已接 1-token 保活重放，OpenAI 兼容端点仍默认关
71. **注入扫描**（工具结果/skill/MCP 输出）— `[默认开]` 多项 injection_scan
72. **不可信内容边界标记** — `[opt-in]` untrusted_boundary / untrusted_result
73. **工具结果裁剪 + 上下文编辑** — `[默认开]` tool_prune
74. **上下文快照 diff** — `[已有]`
75. **image 输入（@图片路径）+ 带图 token 估算** — `[默认开]` image_input
76. **图像按最旧卸载为文本占位** — `[默认开]`
77. **continuation brief / retention guidance / 去重** — `[默认开]`
78. **Model-visible ⟺ logged 不变量** — `[部分]`（会话事件已有，缺显式守卫）
79. **context Epoch / 归一化 / 回复分类纯策略** — `[已有]` agentpolicy

---

## 4. 会话与状态（Session）

80. **append-only JSONL 会话树**（id/parent）— `[已有]`
81. **fork / tree / checkout / rewind** — `[已有]`
82. **SHA-256 checkpoint + undo/retry** — `[已有]`
83. **会话恢复分类**（clean/degraded/interrupted + 公告）— `[已有]`
84. **恢复字节快照 + 回放** — `[opt-in]` request_snapshot / resume_byte_replay
85. **durable 事件最小化 + 投影读模型** — `[部分]`（JSONL+投影，非完整事件溯源）
86. **格式版本与迁移不可变**（只新增后继世代）— `[部分]`
87. **会话归档导出**（HTML/JSON/MD/YAML）— `[已有]` exporthtml 等
88. **会话树 RPC（session.tree）** — `[已有]`
89. **成本对账单一真源** — `[已有]` calcutil
90. **tagged union 事件 + ignorable 语义** — `[部分]`（事件类型固定，缺 ignorable 标记）
91. **结构化压缩摘要保留 + 逐条淘汰** — `[默认开]` compact_drain

---

## 5. 安全与审批（Security）

92. **策略→审批→审计三段式安全链** — `[已有]`
93. **三沙箱模式**（read-only / workspace-write / danger-full-access）— `[已有]`
94. **路径逃逸防护**（.. / 绝对路径 / 4 轮解码 / 符号链接）— `[已有]`
95. **RequireHuman 破窗**（不可被 yolo/auto 自动批准）— `[已有]`
96. **作用域授予 + TTL + 持久化审批** — `[已有]` + `[opt-in]` approval_persist
97. **分阶段审批状态机**（once/always/reject/esc）— `[opt-in]` staged_approval
98. **审批真实 diff + 危险意图摘要 + 默认拒绝** — `[默认开]` approval_diff
99. **危险命令静态高亮** — `[已有]`
100. **脱敏先于审计写入** — `[默认开]` audit_sanitize
101. **凭据加密**（Windows DPAPI，无 cgo）— `[已有]`（非 Windows 明文，见缺口）
102. **声明式网络出口策略 + 代理沙箱 + 域名审批** — `[已有]`（本会话新增接线）
103. **能力分键授权**（read grant 不可复用为 write）— `[已有]`
104. **baseURL 来源追踪 fail-closed** — `[已有]`
105. **提示注入扫描 + 命令风险分级** — `[默认开]` 多项
106. **项目信任门**（--trust-project）— `[已有]`
107. **隐私扫描门禁**（CI 无密钥/路径/邮箱）— `[已有]` privacy-scan.ps1
108. **OS 级沙箱**（Seatbelt/bubblewrap/restricted-token，fail-closed）— `[缺口]`（stdlib-only 取舍）
109. **遥测敏感字段白名单 + 字节上限** — `[部分]`
110. **检查层 Guardian / 输出改写** — `[opt-in]` guardian / hook_rewrite

---

## 6. 扩展机制（Extension）

111. **MCP server 一等公民**（stdio / HTTP-SSE）— `[已有]`
112. **skill（SKILL.md 渐进披露 + 自写）** — `[已有]`
113. **hook 系统**（11 事件 + SHA-256 信任门 + middleware 洋葱链）— `[已有]`
114. **slash command（markdown + 内置）** — `[已有]`
115. **plugin 清单发现/安装/合并/校验** — `[已有]`
116. **插件 marketplace / SDK / 包管理** — `[缺口]`（生态维度取舍，仅 6 分）
117. **统一扩展门面**（发现/信任/生命周期/失败隔离）— `[已有]` extension 包（本会话新增）
118. **能力接缝三角色**（Definition/Provider/Consumer）— `[部分]`（参数注入，未显式三角色）
119. **可逆注册 / disposer** — `[部分]`（pluginapi 注册表，无 disposer）
120. **依赖拓扑声明 + 机器校验** — `[已有]` pluginapi.TopoSort + boot_topo
121. **编译期注册 vs 运行时插件分层** — `[已有]`（Go 静态注册，无热重载）
122. **extension priority：MCP > skill/hook/command > 核心** — `[已有]`（AGENTS.md 原则）
123. **新能力落地层级纪律**（能外置绝不写核心）— `[已有]`
124. **用户可配置 keybinding 注册表**（冲突检测）— `[已有]`（本会话新增）

---

## 7. 交互与界面（UX）

125. **全屏 TUI**（自绘 ANSI、差速渲染、无 WebView）— `[已有]`
126. **MyGo 原生桌面 GUI**（唯一第三方依赖、RPC 驱动）— `[已有]`（Windows-only）
127. **流式 transcript + markdown（表格/diff）** — `[已有]`
128. **审批卡片**（真实 diff、三态、危险默认拒绝）— `[已有]`
129. **输入编辑器**（补全/历史/kill-ring/vim）— `[已有]` + `[opt-in]` vim_mode
130. **`/` 命令补全面板 + 路径/@mention 补全** — `[已有]`
131. **多面板**（tasks/workspace/plan/sessions/commands）— `[已有]`
132. **主题（dark/light/ansi）+ locale（zh-CN/en-US）** — `[已有]`
133. **剪贴板**（Windows CF_UNICODETEXT / 非 Windows OSC 52）— `[已有]`（本会话补 OSC52）
134. **鼠标选择/点击展开/滚动** — `[已有]`（本会话修右键 bug）
135. **每轮统计行 + 推理块折叠/展开** — `[已有]`（本会话修 0ms）
136. **TUI 时间线录制 / docs URI 查阅** — `[已有]` timeline / docs 子命令
137. **状态栏**（用量/缓存/模型/思考/速度/成本/上下文）— `[已有]`（本会话修 high 截断+颜色）
138. **会话选择器**（实时过滤/改名/删除）— `[已有]`
139. **错误可展开详情 + /retry 动作** — `[已有]`
140. **首屏欢迎 + 情境提示** — `[已有]`（欢迎卡）
141. **多前端共享一引擎**（TUI/GUI/CLI/三传输）— `[已有]`
142. **i18n 翻译完整性审计** — `[已有]`（locale parity 逐字节）
143. **全屏搜索 + prompt 跳转（OSC 133）** — `[部分]` CLI `sessions find/--grep` 已覆盖内容搜索；TUI 全屏交互 + OSC 133 未做
144. **IME 光标定位（中文候选窗）** — `[缺口]`

---

## 8. 可观测与诊断（Observability）

145. **结构化日志 + TUI 日志改道**（不画进帧）— `[已有]`
146. **always-on 环形 profiler**（region 耗时/调用数/占比）— `[已有]`
147. **Span/Counter/Histogram（NOOP 默认）** — `[已有]` telemetry
148. **工具耗时直方图 / 调用 trace** — `[默认开]` tool_timing_histogram / tool_call_trace
149. **doctor 诊断命令**（配置/会话健康/credential/LSP）— `[已有]` + `--json`
150. **token/成本统计 + usage 工具** — `[已有]`
151. **错误分类 + 中文短句 + 恢复建议** — `[已有]`
152. **OTEL 三支柱统一 provider** — `[部分]`（本地 telemetry，无 OTLP）
153. **诊断事件不丢 + 状态指示器** — `[已有]`（本会话前序修复）
154. **配置漂移诊断 / 变更 diff** — `[默认开]` config_reload_diff

---

## 9. 开发者体验（DX）

155. **分层配置**（7 层：default/global/project/env/flag/profile+override）— `[已有]`
156. **profile 多档位** — `[已有]`
157. **`argentgo init` 四步向导**（provider 探测/凭据/补写配置/doctor）— `[已有]`
158. **`opt-ins` 单一真源治理**（注册表/文档/读点三方守护）— `[已有]`
159. **默认化原则**（改善体验默认开、`=0` 紧急回退）— `[已有]`（AGENTS.md 铁律）
160. **login / logout / auth（PKCE + 设备码）** — `[已有]`
161. **update 自更新**（GitHub release 检查 + 校验）— `[已有]`
162. **eval 五模式**（11 场景 + --live + compare + ab + history）— `[已有]`
163. **benchmark smoke**（断言即测试）— `[已有]` 43 个 benchmark
164. **completion（shell 补全）** — `[已有]`
165. **config / audit / policy / plugin / inbox / timeline / docs 子命令** — `[已有]`（41 子命令）
166. **中文友好建议**（错误指向具体操作）— `[已有]`
167. **配置 provenance（逐 key origins）+ 逐层指纹** — `[部分]`（有 source 追踪，无完整 origins API）
168. **托管策略压制用户配置** — `[已有]` ARGENT_MANAGED_CONFIG 最高层 + fail-closed
169. **配置即服务而非全局态** — `[部分]`（多数门控仍读进程 env，per-run 关不掉）

---

## 10. 协议与集成（Protocol）

170. **OpenAI Chat Completions** — `[已有]`
171. **Anthropic Messages** — `[已有]`
172. **OpenAI Responses** — `[已有]`
173. **provider 模板（20 个）+ 12 别名 + thinking 映射** — `[已有]`；虚拟模型按任务类别路由 + model_route 持久决策已接线
174. **OAuth / 设备码 / API Key 多认证** — `[已有]`
175. **ACP（Agent Client Protocol）** — `[已有]` session/load 已补齐（复用 SessionStore）
176. **`--serve`（HTTP/SSE + Bearer）+ `--rpc`（换行 JSON）** — `[已有]`
177. **server 事件 hub / 续传 / 过滤 / 限流 / 超时** — `[已有]`
178. **SSE 增量解码 + 失败契约 + `ChatOnce` 非流式回补** — `[已有]`
179. **FIM / 图像 part / 提示缓存 wire 支持** — `[已有]`（提示缓存默认关）
180. **MCP 协议（stdio/HTTP-SSE + 健康诊断）** — `[已有]`
181. **多 SDK / 跨语言边界** — `[部分]`（RPC JSONL，无官方多语言 SDK）
182. **server 契约版本兼容性声明** — `[已有]` /v1/version + /v1/health 报版本

---

## 11. 执行恢复与事件溯源（来自架构级范式）

183. **append-only 事件日志是唯一事实源，消息历史派生** — `[部分]` JSONL 事实源；统一解码边界已建，投影仍分散
184. **durable event 最小化 + 读模型富化（derived 状态）** — `[部分]`
185. **tagged union 事件 + `switch(type)` 窄化** — `[已有]` EventKind 判别联合 + eventKindOf 别名收敛
186. **`ignorable` 标记决定未知事件跳过/拒绝重建** — `[已有]` Record.Ignorable + CheckUnknownKinds fail-closed
187. **纯函数投影 + O(新增) 增量 + generation stamp 失效** — `[部分]` ProjectionCache 已接 messages()（整段模式保证跨记录状态正确）；增量模式限于逐条独立投影
188. **本地操作投影与 durable 事件原子提交** — `[部分]`
189. **统一事件解码边界 + 版本演进升级** — `[已有]` DecodeRecord 统一入口 + MigrateRecords 版本迁移
190. **按聚合的有序 log 读取 + 分页 + watermark** — `[已有]` ReadPage 分页游标 + Page.Watermark 位置标记
191. **格式版本单调 + 相邻迁移只新增后继** — `[部分]`
192. **事件族共享稳定业务 id** — `[已有]` correlationIDOf：载荷 toolCallId/attemptId → 记录 id 回落
193. **投递与执行分离 + 幂等 ID（first-admission-wins）** — `[已有]` inbox.Admit 幂等接纳 + runner 持久 admission
194. **write-ahead claim 崩溃恢复（终态释放/中断保留）** — `[部分]` turn lease + claims；账本不可写时降级为进程内去重（不再无条件放行）
195. **背压（ack 窗口 + 队列折叠）** — `[已有]` EventHub 满缓冲订阅者淘汰 + 终态预留槽
196. **事件等待用索引唤醒，禁 backoff/轮询** — `[部分]`（channel select，无索引唤醒）
197. **断链恢复 = 重持有 + 快照替换，错误看起来像掉线** — `[部分]`（snapshot store last-good）
198. **跨二进制/跨版本不承诺回放** — `[部分]`（未显式声明）
199. **领域级 goal 状态与调度器分离** — `[部分]`（/goal 有界循环，无版本化 goal state）

---

## 12. 配置与信任（架构级范式）

200. **8 层配置合并 + 顶层覆盖底层** — `[部分]`（7 层，无 MDM/enterprise cloud）
201. **provenance 逐 key origins + 逐层指纹** — `[部分]`
202. **托管策略压制用户/项目配置** — `[已有]` ARGENT_MANAGED_CONFIG 压制层
203. **配置显式 `resolve(request)` + 禁止隐藏 `?? default`** — `[部分]`（多数门控读 env，无 resolve）
204. **配置错误最早可解析点大声失败** — `[部分]`（fail-closed 诊断已有）
205. **项目信任门（marker/git checkout 才持久化 trust）** — `[已有]` --trust-project
206. **特性开关纪律（风险行为需 flag）** — `[已有]`（默认化原则 + =0 回退）
207. **配置组合分层（profile→bundle→patch→overlay）** — `[缺口]`（大而全产品取舍）
208. **上下文文件多 aliases**（AGENTS/CLAUDE/CRUSH/GEMINI + .local）— `[部分]`（AGENTS/CLAUDE）

---

## 13. 性能原语（Performance）

209. **热路径同步、I/O 异步隔离** — `[已有]`（append 锁 + 异步通知）
210. **进程内热路径原语**（grep/glob/fd 同进程 + 共享缓存 + 取消透传）— `[已有]`（本会话前序吸收）
211. **stamp 缓存正确性**（inode/mtime/ctime，无 TTL）— `[已有]` repo_map_cache
212. **context 取消传播到所有 I/O** — `[已有]`
213. **并发 worker + 多 Session 并行 + 同 Session 合并** — `[部分]`（工具并行有，Session 合并无）
214. **超时预算分层**（close/ready/reload/heartbeat）— `[部分]`（proc 超时有，无统一分层）
215. **缓存 WeakMap + GC 测试纪律** — `[部分]`（sync.Pool 有，无 GC 测试门禁）
216. **预热 / 懒加载 / smoke 探针** — `[部分]`（LSP lazy，无系统预热）
217. **性能门禁可度量**（benchmark 预算）— `[已有]` benchmark smoke
218. **大文件读取上限（编辑10MB/shell10MB/context4MB）+ 流式 hash/gzip** — `[已有]`（本会话 P1 新增）
219. **测量驱动的构建配置** — `[部分]`（-trimpath -ldflags，无 profile 分级）

---

## 14. 测试纪律（Testing）

220. **golden/snapshot 字节锁定**（TUI 帧/规格）— `[已有]` 逐字节 parity + frame 测试
221. **per-file 100% 覆盖率门禁** — `[缺口]`（dsh 有，本仓未强制）
222. **真实入口路径 + 真实实现优先于 mock** — `[已有]`（inject  seam，少 mock）
223. **属性/模糊测试（种子语料）** — `[部分]` security-audit.ps1 fuzz seed
224. **契约级测试命名 + 禁 tautology/源码 grep 测试** — `[部分]`（大量表驱动，无强制契约命名）
225. **CI 门禁矩阵 + 验证分层**（快/慢分离）— `[已有]` AGENTS.md 分层
226. **多前端投影同步更新** — `[部分]`（TUI/native 事件 case 有 AST 穷举守卫）
227. **架构守卫机器可执行**（import/cgo/环）— `[已有]` archguard
228. **原语门禁**（新增导出函数须接线或登记）— `[已有]` primitive_guard
229. **发布门禁**（行数锚点 + 版本一致性）— `[已有]` check-release-consistency
230. **变异反证确认测试有效** — `[部分]`（部分 flaky 修复用变异验证）

---

## 15. 工程质量（Engineering）

231. **零新依赖 / stdlib-only 核心** — `[已有]`（native/ 唯一例外）
232. **无 cgo + 交叉编译 + 单二进制** — `[已有]`（5 平台 matrix）
233. **架构守卫**（分层规则 + 三色 DFS 环检测）— `[已有]`
234. **API 稳定性 + 弃用策略** — `[部分]`（无 deprecated alias 机制）
235. **严格类型 + 边界一次性校验** — `[已有]`（Go 静态类型 + %w 包装）
236. **依赖供应链纪律**（pin/lockfile/allowlist）— `[部分]`（零依赖降低风险）
237. **文档/代码同改 + 漂移守卫** — `[已有]` optin_docs_guard / parity_test
238. **"门禁通过 ≠ 接线完成"意识**（grep 生产调用点）— `[已有]`（多次实践）
239. **隐私扫描门禁** — `[已有]`
240. **反模式清单通过守卫封死** — `[部分]`（部分通过 archguard/测试，部分靠纪律）

---

## 16. 反模式清单（跨竞品一致否定，不应做）

1. **内存内 tool loop 绕过持久化历史** — 每步必须从重载历史继续。`[反模式]`
2. **把 claim 当 exactly-once / 集群 fencing** — claim 是恢复标记，不是所有权。`[反模式]`
3. **服务端等待插件加载 / 慢插件变 503** — 异步加载，客户端自行等 updated。`[反模式]`
4. **隐藏默认（`?? default`）与插件内硬编码 tunable** — 显式 resolve。`[反模式]`
5. **静默跳过缺失 referent / 配置错误不报** — 最早点大声失败。`[反模式]`
6. **未知事件静默丢弃**（缺 ignorable 却跳过）— 未知类型应拒绝重建。`[反模式]`
7. **模块级强缓存 / 无 GC 测试** — 按 owner WeakMap + GC test。`[反模式]`
8. **source-grep 式测试 / mock.module / tautology** — 契约级测试。`[反模式]`
9. **手写生成的客户端/SDK 文件** — codegen 单一来源。`[反模式]`
10. **复制 context key / 低层反 import 上层成环** — 上下文 key 单一归属。`[反模式]`
11. **为理论竞态/安全 edge 静默扩张复杂度** — 有实际证据才加。`[反模式]`
12. **照搬框架绑定的交互**（opentui/React/Electron/HMR）— 违背 stdlib-only 单二进制。`[反模式]`

---

## 17. 优先投入排序（设计 harness 的底层能力地基）

按"地基依赖顺序 + 竞品共识强度 + 返工成本"排序：

1. **事件溯源 + 投影底座**（#183-192）— append-only log、tagged union、ignorable、原子投影、统一解码。会话/恢复/遥测/fork 的共同根，最先定，返工代价最高。argentgo 当前 `[部分]`，是最大结构性缺口。
2. **无状态循环 + inbox 分离 + claim**（#45, #193-196）— 每步重载历史、幂等投递、进程内 claim 恢复、背压。与 #1 共同构成可恢复执行核心。
3. **能力接缝 + 可逆注册 + 注入拓扑**（#116-123）— 三角色 seam、disposer、inject 依赖、MCP>skill>core 落地层级。决定后续功能如何增量添加。argentgo 已有门面但 disposer/三角色仍 `[部分]`。
4. **分层配置 + provenance + 项目信任门**（#155-208）— 多用户/多项目产品越早越好。
5. **安全链与最小权限**（#92-110）— 策略→审批→审计、沙箱三模式、脱敏先于持久化、不可信标记。事后补代价极大。argentgo 此项护城河最强（9/10）。
6. **测试纪律**（#220-230）— golden 字节锁定、契约测试、benchmark 即测试、CI 矩阵。与 #1/#2 同步建立，否则事件语义无法锁定。
7. **可观测性**（#145-154）— 结构化日志、telemetry、错误分类、诊断命令。在 #1 事件流上挂载成本最低。
8. **性能原语**（#209-219）— 增量投影/缓存 stamp、context 取消、并发 worker、超时预算。接口随 #1/#2 设计，实现可后置。
9. **多运行时/多前端**（#141, #170-182）— 单引擎多传输、依赖方向守卫。内核稳定后再铺前端。argentgo 已相当完整。
10. **工程质量护栏**（#231-240）— 零依赖、cross-compile、archguard。贯穿全程，作为 CI/规范落地。

> **一句话**：先把"事件日志 + 恢复循环 + 接缝注入"三件事做对，再用"配置/安全/测试/可观测"四套护栏锁住，最后扩展多前端与工程纪律。反模式清单应在第一版就通过架构守卫和测试封死。

---

## 18. argentgo 状态总览

| 维度 | 条目数 | 状态分布 |
|---|---:|---|
| 工具系统 | 43 | 绝大多数 `[已有]`/`[默认开]`，少数 `[opt-in]` |
| Agent 循环 | 21 | 核心完整，claim/backpressure/Code Mode `[部分]`/`[缺口]` |
| 上下文管理 | 15 | 完整，提示缓存 `[部分]` |
| 会话与状态 | 12 | 完整，事件溯源语义 `[部分]` |
| 安全与审批 | 19 | 护城河（9/10），OS 沙箱 `[缺口]` |
| 扩展机制 | 14 | 完整，marketplace/SDK `[缺口]` |
| 交互界面 | 20 | 双前端完整，搜索/IME `[缺口]` |
| 可观测 | 10 | 本地完整，OTLP `[部分]` |
| DX / 协议 / 性能 / 测试 / 工程 | 85+ | 工程底座强项，个别分发/覆盖率 `[缺口]` |

**基础数据**（2026-11-24）：v0.8.2 · 504,133 行 · 1,631 测试文件 · 34 默认工具 + 18 显式/opt-in 工具 · 51 internal 包 · 41 CLI 子命令 · 173 独立 opt-in 门控。

**主要缺口**（按重要性）：① 完整事件溯源+投影底座（结构性，返工成本最高）；② backpressure/claim 语义；③ OS 级沙箱（stdlib-only 取舍）；④ marketplace/多语言 SDK/社区运营（需真实发布运营，非本地代码）；⑤ 提示缓存默认开、web_search 内置后端、多语言/多平台 GUI（真实环境/资源投入）；⑥ 文档漂移（DAP/浏览器/computer 已实现但 STATUS/TECHNICAL 仍写不做，需同步）。

> 本清单是决策参考，不直接改代码。每条状态可据源码复核；"缺口"项多已被竞品验证但需按本项目约束（stdlib-only/单二进制/零依赖）裁剪后决定是否落地。
