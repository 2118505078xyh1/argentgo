package cli

// 本文件是「独立门控」的单一真源。所有不经过 ARGENT_OPT_INS 集中注册表、
// 而是由各自 ARGENT_* 环境变量控制的选择性特性都在这里登记。
//
// opt-ins 命令用它展示已知门控，optin_docs_guard_test.go 用它守护
// docs/OPT_INS.md 与源码读点的一致性。新增或删除独立门控时只需改这一张表：
// 守卫会自动发现「表中有、文档缺」以及「文档有、源码/表皆无」两种漂移。

// IndependentOptIn 描述一个独立门控变量。
//
// Key 是稳定短键名（如 approval_diff），Env 是逐字对应源码读点的环境变量名，
// Desc 是中文说明。OptInKey 非空时表示该门控同时接受 ARGENT_OPT_INS 列表中的
// 等价键（双激活面）。Historical 仅用于 ARGENT_POST_EDIT_VERIFY 这类
// 「文档记载但源码无真实读点」的历史占位，守卫会跳过其源码读点校验。
type IndependentOptIn struct {
	Key        string // 稳定短键名（蛇形），用于展示与查找
	Env        string // 环境变量名（单一真源键，逐字对应源码读点）
	Name       string // 中文名称
	OptInKey   string // 统一列表 ARGENT_OPT_INS 中的等价键；空表示仅独立 env
	Enable     string // 开启条件摘要（中文）
	Desc       string // 中文说明
	Historical bool   // 历史占位：仅文档记载、源码无真实读点
}

// IndependentOptIns 是全部独立门控的登记表。Env 与 Key 均唯一。
//
// 分类仅为阅读方便：交互/展示、上下文/压缩、安全/审批、shell/工具、
// MCP/产物、评测/其它。表本身不依赖分类字段做任何判定。
var IndependentOptIns = []IndependentOptIn{
	// ---- 交互 / 展示 ----
	{Key: "cli_hints", Env: "ARGENT_CLI_HINTS", Name: "CLI 错误修复建议", OptInKey: "cli_hints", Enable: "=1 或 ARGENT_OPT_INS=cli_hints", Desc: "CLI 已知错误后追加本地化修复建议。"},
	{Key: "approve_danger", Env: "ARGENT_APPROVE_DANGER", Name: "TUI 危险命令原因展示", Enable: "=1", Desc: "TUI 审批弹窗展示稳定中文危险原因并预选拒绝。"},
	{Key: "approval_diff", Env: "ARGENT_APPROVAL_DIFF", Name: "审批卡 diff 展示", Enable: "=1", Desc: "TUI 审批卡渲染有界的增删行摘要（diff 行）。"},
	{Key: "tui_usage", Env: "ARGENT_TUI_USAGE", Name: "TUI 用量显示", Enable: "=1", Desc: "TUI 头部显示本轮 token 用量与缓存命中。"},
	{Key: "tui_prompt_history", Env: "ARGENT_TUI_PROMPT_HISTORY", Name: "TUI 输入历史持久化", Enable: "=1", Desc: "TUI 提交的输入写入存储目录 prompts.jsonl，重启后 Up 可召回；关闭时无文件读写、帧字节不变。"},
	{Key: "vim_mode", Env: "ARGENT_VIM_MODE", Name: "vim 键位", Enable: "=1", Desc: "TUI 输入行切换到 vim 键位，默认经典输入逐字节不变。"},
	{Key: "path_completion", Env: "ARGENT_PATH_COMPLETION", Name: "内联路径补全", Enable: "=1", Desc: "TUI 输入行启用内联路径补全，默认 slash 补全逐字节不变。"},
	{Key: "cache_visible", Env: "ARGENT_CACHE_VISIBLE", Name: "缓存能力提示", Enable: "=1", Desc: "--tokens 报告追加一行静态缓存能力提示。"},
	{Key: "sessions_head_tail", Env: "ARGENT_SESSIONS_HEAD_TAIL", Name: "会话列表首尾预览", Enable: "=1", Desc: "会话列表显示首尾消息预览。"},
	{Key: "pty_interactive", Env: "ARGENT_PTY_INTERACTIVE", Name: "交互式 PTY 子命令", Enable: "=1", Desc: "注册 argentgo pty 子命令，在真实伪终端中运行程序（剥离终端查询序列、UTF-8 增量解码、ctx 取消回收）；仅开启时注册，默认 usage/help/补全字节不变。"},
	{Key: "locale", Env: "ARGENT_LOCALE", Name: "Native 界面语言", Enable: "en-US 及其别名（en/en_us/English）", Desc: "原生桌面窗口的硬编码中文文案切到英文（复用 i18n 目录同值，native 自包含无违规依赖）；空值/中文/其它值一律保持原中文，逐字节不变。"},

	// ---- 上下文 / 压缩 / 会话 ----
	{Key: "continuation_brief", Env: "ARGENT_CONTINUATION_BRIEF", Name: "会话续接摘要", Enable: "=1", Desc: "resume/追问时向 system 提示注入上一段会话的有界摘要。"},
	{Key: "retain_guidance", Env: "ARGENT_RETAIN_GUIDANCE", Name: "纠正提示压缩存活", Enable: "=1", Desc: "保留纠正 nudge 记录的压缩存活标记，关闭时记录形状逐字节不变。"},
	{Key: "guidance_dedupe", Env: "ARGENT_GUIDANCE_DEDUPE", Name: "失败指引签名去重", Enable: "=1", Desc: "连续失败期间同一种错误签名只注入一次恢复指引，避免长对话堆积雷同指引；关闭时每轮照常注入，消息流逐字节不变。"},
	{Key: "summary_schema", Env: "ARGENT_SUMMARY_SCHEMA", Name: "压缩摘要固定 schema", Enable: "=1", Desc: "摘要产出后按固定顺序补齐 Goal/State/Next/Files 小节。"},
	{Key: "budget_projection", Env: "ARGENT_BUDGET_PROJECTION", Name: "消息预算投影", Enable: "=1", Desc: "Messages 前按 rune 预算投影，保护最新用户输入与未配对的活尾工具调用。"},
	{Key: "budget_projection_budget", Env: "ARGENT_BUDGET_PROJECTION_BUDGET", Name: "消息预算投影预算", Enable: "正整数（缺省 32000）", Desc: "指定投影的 rune 预算，非正或不可解析时回退保守默认。"},
	{Key: "foveated_archive", Env: "ARGENT_FOVEATED_ARCHIVE", Name: "foveated 多保真归档", Enable: "=1", Desc: "Messages 读路径改用两端逐字保真、中段按预算稀疏并插入可解析 gap 标记的纯文本多保真视图（TODO #134 第一步）；切点落在工具配对单元边界、保留压缩摘要，零 wire 变化。关闭时逐字节不变。"},
	{Key: "background_compact", Env: "ARGENT_BACKGROUND_COMPACT", Name: "后台预压缩", Enable: "=1", Desc: "接近压缩阈值（80%）时后台goroutine提前准备压缩视图，到阈值处校验锚点/tail 后零阻塞切换；tail 过长、失败或连续失败达上限时回退同步路径。默认关闭时同步压缩逐字节不变。"},
	{Key: "context_calibration", Env: "ARGENT_CONTEXT_CALIBRATION", Name: "上下文计量校准", Enable: "=1", Desc: "用提供方实测 totalTokens 锚点与文本估算的比率（钳到 1..4）校准压缩判定，并提供预留余量上限（max(15%,16000)）与逐级缩减窗口；无锚点/默认关闭时阈值逐字节不变。"},
	{Key: "exec_session", Env: "ARGENT_EXEC_SESSION", Name: "统一 exec 会话", Enable: "=1", Desc: "bash 支持 session_id/write_stdin/yield_time_ms/TTY，注册 write_stdin 工具；会话注册表有界输出与空闲回收，Ctrl-C 按会话精确 kill。关闭时 schema、参数解析与 Specs() 逐字节不变。"},
	{Key: "approval_persist", Env: "ARGENT_APPROVAL_PERSIST", Name: "持久化审批规则", Enable: "=1", Desc: "在作用域审批器之上叠加跨会话的 always/deny 规则，落盘 <storage>/approvals.jsonl（只记工具名，不记自由文本参数）；文件损坏时 fail-closed。关闭时不构造该审批器、零文件 I/O、逐字节不变。"},
	{Key: "recovery_replay", Env: "ARGENT_RECOVERY_REPLAY", Name: "恢复重放后缀", Enable: "=1", Desc: "恢复路径上追加重放后缀。"},
	{Key: "recovery_detail", Env: "ARGENT_RECOVERY_DETAIL", Name: "恢复细节后缀", Enable: "=1", Desc: "恢复路径上把一次性分类得到的 RecoveryPlan 细节（类别/断链/孤儿调用/孤儿结果）作为文本后缀追加到公告；关闭时公告逐字节不变。"},
	{Key: "checkpoint_guard", Env: "ARGENT_CHECKPOINT_GUARD", Name: "checkout 还原目标守卫", Enable: "=1", Desc: "sessions checkout 时对转录目标做只读校验，拒绝逃逸或已消失的目标；关闭时 checkout 逐字节不变。"},
	{Key: "compaction_event", Env: "ARGENT_COMPACTION_EVENT", Name: "压缩事件落盘", Enable: "=1", Desc: "压缩时在 KindCompaction 记录后额外追加一条 KindCondensation 记录（含被遗忘 ID、摘要、切分点），该记录不进入模型可见对话；关闭时压缩写入逐字节不变。"},
	{Key: "tool_prune", Env: "ARGENT_TOOL_PRUNE", Name: "工具结果压力裁剪", Enable: "=1", Desc: "在预算投影前对超长工具结果保留头尾、中间替换为裁剪标记（按 rune，幂等），预算复用 ARGENT_BUDGET_PROJECTION_BUDGET；关闭时消息流逐字节不变。"},
	{Key: "image_input", Env: "ARGENT_IMAGE_INPUT", Name: "图像输入", Enable: "=1", Desc: "从用户原文扫描 @图片路径并作为图像 part 注入首条用户消息；关闭时 InitialImages 为 nil，首条消息逐字节不变。"},

	// ---- 安全 / 审批 ----
	{Key: "inspection", Env: "ARGENT_INSPECTION", Name: "安全失败检查层总开关", Enable: "=1", Desc: "启用检查链总开关；关闭时安全链裸链运行，Guardian 绝不生效。"},
	{Key: "guardian", Env: "ARGENT_GUARDIAN", Name: "Guardian 审阅", Enable: "=1（且 INSPECTION=1、次要模型非空）", Desc: "次要模型审阅安全链结果，两开关为「与」关系。"},
	{Key: "hook_verdict", Env: "ARGENT_HOOK_VERDICT", Name: "钩子权限裁决折叠", Enable: "=1", Desc: "启用 stdout permissionDecision 裁决：deny 拦截工具调用，allow 仅在无 exit 2 拦截时解除；同时注入审计与补 session/cwd 中间件。默认关闭时 permissionDecision 被忽略。"},
	{Key: "guardrails", Env: "ARGENT_GUARDRAILS", Name: "护栏运行时模式", Enable: "enforce / shadow", Desc: "选择护栏运行时模式，由 normalizeGuardrailMode 归一化。"},
	{Key: "patch_policy_paths", Env: "ARGENT_PATCH_POLICY_PATHS", Name: "apply_patch 目标路径策略校验", Enable: "=1", Desc: "从补丁正文解析真实目标文件，逐条走 denyPaths/allowPaths 等路径策略；关闭时 apply_patch 的补丁正文不参与路径规则，安全链行为逐字节不变。"},
	{Key: "tool_arg_feedback", Env: "ARGENT_TOOL_ARG_FEEDBACK", Name: "畸形工具参数纠错回灌", Enable: "=1", Desc: "模型发出的参数非空却不是合法 JSON 时不执行工具，把原始片段（脱敏限长）作为错误结果回灌，让模型重发；关闭时仍按历史行为回退为 {} 后派发，逐字节不变。"},
	{Key: "multimodal_trim", Env: "ARGENT_MULTIMODAL_TRIM", Name: "带图消息 token 估算", Enable: "=1", Desc: "把 ContentParts 的文本与图像 part 计入 token 估算，使带图会话的上下文压力与裁剪决策准确；关闭时带图消息仍只算框架开销，逐字节不变。"},
	{Key: "grep_per_file", Env: "ARGENT_GREP_PER_FILE", Name: "grep 单文件匹配上限", Enable: "正整数（缺省 0 禁用）", Desc: "限制单个文件的匹配数，某文件达上限后跳过其余行但继续遍历其它文件，保留文件多样性；关闭时仍由全局上限决定，逐字节不变。"},
	{Key: "block_generated_edits", Env: "ARGENT_BLOCK_GENERATED_EDITS", Name: "禁止编辑生成文件", Enable: "=1", Desc: "edit/write/apply_patch 在写入前按文件名模式与头部标记拒绝编辑生成物，指引改源文件后重新生成；默认关闭时编辑行为逐字节不变。"},
	{Key: "elide_data_urls", Env: "ARGENT_ELIDE_DATA_URLS", Name: "裁剪前替换 data URL", Enable: "=1", Desc: "在工具结果裁剪前把整段 base64 data URL 原子替换为保留 MIME 与长度的占位，避免切断图片引用；默认关闭时裁剪逐字节不变。"},
	{Key: "hook_rewrite", Env: "ARGENT_HOOK_REWRITE", Name: "钩子输入/输出改写", Enable: "=1", Desc: "应用 PreToolUse 钩子声明的 updatedInput/updatedOutput：输入改写与审批/执行使用同一份字节，输出改写替换回灌正文；默认关闭时参数与输出逐字节不变。"},
	{Key: "compaction_hook", Env: "ARGENT_COMPACTION_HOOK", Name: "压缩事件钩子", Enable: "=1", Desc: "压缩前触发 PreCompact（可阻断压缩并公告原因），落盘后触发 PostCompact；默认关闭时压缩路径无任何钩子调用，逐字节不变。"},
	{Key: "compaction_todo", Env: "ARGENT_COMPACTION_TODO", Name: "压缩保留任务进度", Enable: "=1", Desc: "压缩时把会话 todos.json 以 [x]/[>]/[ ] 行写入 Compaction.Todo 并镜像进摘要，使恢复后模型知道任务进度；默认关闭时压缩记录逐字节不变。"},
	{Key: "branch_summary", Env: "ARGENT_BRANCH_SUMMARY", Name: "分支切换摘要", Enable: "=1", Desc: "Checkout 时为被弃分支生成确定性摘要并追加到新活动分支，使模型知道放弃路径上做过什么；默认关闭时 checkout 与消息投影逐字节不变。"},
	{Key: "read_before_write", Env: "ARGENT_READ_BEFORE_WRITE", Name: "读后写新鲜度校验", Enable: "=1", Desc: "read 成功后登记文件快照，edit/write 在外部改动导致快照过期时拒绝写入并提示重读；默认关闭时无账本、无检查，工具行为逐字节不变。"},
	{Key: "mcp_annotation_approval", Env: "ARGENT_MCP_ANNOTATION_APPROVAL", Name: "MCP 破坏性工具审批", Enable: "=1", Desc: "按 MCP annotations 把声明 destructiveHint 且非 readOnly 的工具纳入强制审批（即便策略允许也询问）；默认关闭时仅按工具名白名单决定审批，逐字节不变。"},
	{Key: "mcp_gateway", Env: "ARGENT_MCP_GATEWAY", Name: "MCP 单网关工具", Enable: "=1", Desc: "只公布一个固定 mcp 工具（发现走文本返回、服务首次使用时才连接），工具列表整会话不变以稳定缓存前缀；派生名审批、只读沙箱与安全门控照旧。默认关闭时逐字节不变。"},
	{Key: "event_log", Env: "ARGENT_EVENT_LOG", Name: "事件日志续传", Enable: "=1", Desc: "把 hub 事件以 JSONL 落盘，SSE 支持 Last-Event-ID 在超出内存窗口后续放；默认关闭时纯内存、SSE 帧无 id:，逐字节不变。"},
	{Key: "failure_signals", Env: "ARGENT_FAILURE_SIGNALS", Name: "失败信号压缩证据", Enable: "=1", Desc: "从工具结果与消息文本提取 is_error/非零 exit/timed out/失败词信号并入压缩 Failures；默认关闭时不提取，压缩逐字节不变。"},
	{Key: "summary_empty_retry", Env: "ARGENT_SUMMARY_EMPTY_RETRY", Name: "空摘要重试", Enable: "=1", Desc: "压缩摘要为空时少丢一条消息重算一次摘要；默认关闭时使用原摘要，压缩逐字节不变。"},
	{Key: "mcp_injection_medium", Env: "ARGENT_MCP_INJECTION_MEDIUM", Name: "MCP 中风险注入警示", Enable: "=1", Desc: "MCP 工具输出的中风险注入命中也加可见警示；默认关闭时仅高风险警示，输出逐字节不变。"},
	{Key: "mcp_tool_cache_ttl", Env: "ARGENT_MCP_TOOL_CACHE_TTL", Name: "MCP 工具列表缓存", Enable: "正整数秒（缺省 0 禁用）", Desc: "为 MCP 工具发现结果启用带 TTL 的缓存，有效期内复用列表；默认 0 禁用时每次重新发现，行为不变。"},
	{Key: "parse_regression_guard", Env: "ARGENT_PARSE_REGRESSION_GUARD", Name: "编辑语法劣化检查", Enable: "=1", Desc: "edit/write/apply_patch 在写入前用 go/parser 检查：原本合法的 .go 文件若改后语法错误则拒绝；默认关闭时不检查，写入逐字节不变。"},
	{Key: "approval_preview", Env: "ARGENT_APPROVAL_PREVIEW", Name: "审批差异预览", Enable: "=1", Desc: "写类工具的审批提示附带 +N/-M 行数摘要，帮助判断改动内容；不改变审批判定，默认关闭时提示逐字节不变。"},
	{Key: "mcp_output_limit", Env: "ARGENT_MCP_OUTPUT_LIMIT_BYTES", Name: "MCP 输出硬上限", Enable: "正整数字节（缺省 0 禁用）", Desc: "单次 MCP 调用输出超过上限时保留头尾并附恢复标记，防止话痨服务器淹没上下文；默认 0 时行为不变。"},
	{Key: "verify_write", Env: "ARGENT_VERIFY_WRITE", Name: "写后漂移校验", Enable: "=1", Desc: "写入后重读文件比对实际字节，漂移时附按落盘字节重算的 hashline 锚点警告；默认关闭时零额外读取，结果逐字节不变。"},
	{Key: "lsp_sanitize_env", Env: "ARGENT_LSP_SANITIZE_ENV", Name: "LSP 环境净化", Enable: "=1", Desc: "启动语言服务器时用 proc 净化环境，避免继承宿主私有会话管道变量；默认关闭时按原方式拼环境。"},
	{Key: "lsp_kill_tree", Env: "ARGENT_LSP_KILL_TREE", Name: "LSP 整树终止", Enable: "=1", Desc: "关闭语言服务器时终止整棵进程树，避免孙进程变孤儿；默认关闭时只杀直接进程。"},
	{Key: "plugin_output_schema", Env: "ARGENT_PLUGIN_OUTPUT_SCHEMA", Name: "插件输出 schema 校验", Enable: "=1", Desc: "声明 OutputSchema 的插件工具输出必须是合法 JSON，违反时转成模型可读错误；默认关闭时不校验，内置工具不受影响。"},
	{Key: "hook_lifecycle", Env: "ARGENT_HOOK_LIFECYCLE", Name: "生命周期 hook 事件", Enable: "=1", Desc: "把 agent 轮次边界/中断/子 agent 结束事件映射为 TurnStart/TurnEnd/Interrupt/StopFailure/SubagentStop hook；默认关闭时无额外 hook 调用。"},
	{Key: "audit_sanitize", Env: "ARGENT_AUDIT_SANITIZE", Name: "审计命令脱敏", Enable: "=1", Desc: "审计日志写入前用 bash.SanitizeForLog 脱敏 reason 中的 VAR=值/口令状串；默认关闭时只走既有 RedactJSON。"},
	{Key: "tui_badge_contrast", Env: "ARGENT_TUI_BADGE_BG", Name: "TUI 徽章对比度", Enable: "三个 BADGE_* env 同时提供", Desc: "为自定义主题提供徽章 hex 配色（背景/墨色/备选墨色），由 LegibleInk 选可读墨色；默认未配置时帧逐字节不变。"},
	{Key: "mcp_call_profile", Env: "ARGENT_MCP_CALL_PROFILE", Name: "MCP 每次调用权限 profile", Enable: "=1", Desc: "声明 readOnlyHint 的 MCP 工具按每次调用注入只读 profile，拒绝修改类工具（只收紧不放宽）；默认关闭时行为不变。"},
	{Key: "telemetry", Env: "ARGENT_TELEMETRY", Name: "类型化观测", Enable: "=1", Desc: "用统一 Span/Counter/Histogram 契约记录每轮时间；默认使用零分配 NOOP，事件/日志/会话字节不变。"},
	{Key: "retry_omit", Env: "ARGENT_RETRY_OMIT", Name: "重试前持久上下文省略", Enable: "=1", Desc: "可重试失败后切换模型前，由回调把失败助手消息及工具结果以 ContextEdit 永久从模型可见上下文省略，并按指数退避等待（可被 ctx 取消）；关闭时逐字节不变。"},
	{Key: "live_steer", Env: "ARGENT_LIVE_STEER", Name: "供应商原生流中重定向", Enable: "=1", Desc: "Responses 协议 SSE 事件边界检查排队纠正文本，按工具调用状态记 accepted/deferred 并经 SteerHook 上报；不改线协议字节；默认关闭时零开销、逐字节不变。"},
	{Key: "suspended_job", Env: "ARGENT_SUSPENDED_JOB", Name: "Windows 挂起启动作业对象", Enable: "=1", Desc: "Windows 上需要整树终止时改用 CREATE_SUSPENDED 启动：先纳入作业对象再恢复主线程，入作业失败即终止从未运行的进程；默认关闭时启动路径逐字节不变。"},
	{Key: "approval_ttl", Env: "ARGENT_APPROVAL_TTL", Name: "TTL 审批门控", Enable: "时长（0 或空关闭）", Desc: "带 TTL 的审批门控。"},
	{Key: "auto_decide", Env: "ARGENT_AUTO_DECIDE", Name: "自动决策", Enable: "=1", Desc: "启用自动决策行为，仅 =1 改变行为。"},
	{Key: "no_intent_schema", Env: "ARGENT_NO_INTENT_SCHEMA", Name: "关闭意图 schema", Enable: "=1", Desc: "工具注册表门控：关闭意图 schema。"},
	{Key: "boot_topo", Env: "ARGENT_BOOT_TOPO", Name: "启动拓扑校验", Enable: "=1", Desc: "用依赖图推导构造顺序并与历史比对，漂移则 fail-closed。"},
	{Key: "config_shadow_check", Env: "ARGENT_CONFIG_SHADOW_CHECK", Name: "配置遮蔽告警", Enable: "=1", Desc: "输出配置来源遮蔽告警。"},
	{Key: "plan_mode", Env: "ARGENT_PLAN_MODE", Name: "只规划模式", Enable: "=1", Desc: "本次运行只做规划，不改任何文件。"},
	{Key: "checkpoint_workspace_diff", Env: "ARGENT_CHECKPOINT_WORKSPACE_DIFF", Name: "工作区差异检查点", Enable: "=1", Desc: "把 shell 等间接文件改动也纳入检查点，使 /undo 可还原。"},
	{Key: "trust_project", Env: "ARGENT_TRUST_PROJECT", Name: "项目级端点改写信任", Enable: "true/yes/on", Desc: "显式放行项目级端点改写；默认不信任。"},
	{Key: "skill_injection_scan", Env: "ARGENT_SKILL_INJECTION_SCAN", Name: "技能索引注入扫描", Enable: "=1", Desc: "渲染技能索引前扫描 desc/when 字段的语义注入，命中高风险时把整字段替换为中性占位；关闭时渲染逐字节不变。"},

	// ---- 系统提示 / 编辑后诊断 ----
	{Key: "edit_guide", Env: "ARGENT_EDIT_GUIDE", Name: "系统提示编辑指南", Enable: "=1", Desc: "在系统提示中追加编辑指南。"},
	{Key: "edit_diagnostics", Env: "ARGENT_EDIT_DIAGNOSTICS", Name: "编辑后诊断", Enable: "=1", Desc: "启用编辑后诊断路径。"},
	{Key: "repo_map_hint", Env: "ARGENT_REPO_MAP_HINT", Name: "repo map 授权提示", Enable: "=1", Desc: "系统提示追加「文件出现在 repo map 不等于获得编辑授权」。"},
	{Key: "verify_autorun_hint", Env: "ARGENT_VERIFY_AUTORUN_HINT", Name: "验证自动运行提示", Enable: "=1", Desc: "系统提示追加验证自动运行提示。"},
	{Key: "verify_outcome", Env: "ARGENT_VERIFY_OUTCOME", Name: "验证结果分类标记", Enable: "=1", Desc: "验证输出打上结果分类标记。"},
	{Key: "rule_discovery", Env: "ARGENT_RULE_DISCOVERY", Name: "规则文件发现元数据提示", Enable: "true/yes/on", Desc: "扫描用户级与项目级规则文件，把文件名与作用域渲染为只列名、不加载全文的 system 提示片段；关闭时提示逐字节不变。"},
	{Key: "post_edit_verify", Env: "ARGENT_POST_EDIT_VERIFY", Name: "编辑后验证（历史占位）", Enable: "无（未接线）", Desc: "仅历史注释提及，无真实读点；真实开关是 opt-in auto_verify 与 ARGENT_EDIT_DIAGNOSTICS。", Historical: true},

	// ---- shell / 工具 ----
	{Key: "jobs", Env: "ARGENT_JOBS", Name: "后台作业注册表", OptInKey: "jobs", Enable: "=1 或 ARGENT_OPT_INS=jobs", Desc: "把长命令提升为可查询的后台作业，退出时回收仍存活的进程。"},
	{Key: "persistent_shell", Env: "ARGENT_PERSISTENT_SHELL", Name: "持久化 shell 会话", Enable: "=1", Desc: "bash 命令在长驻 shell 会话中执行以复用 cd/venv 等状态；阶段1不支持 stdin 消费型命令，只杀直接 shell，丢弃 stderr。"},
	{Key: "builtin_shell", Env: "ARGENT_BUILTIN_SHELL", Name: "内建 shell 兜底", Enable: "=1", Desc: "切换到纯标准库自研 shell 解释器。"},
	{Key: "bash_use_proc", Env: "ARGENT_BASH_USE_PROC", Name: "bash 进程硬化", Enable: "=1", Desc: "默认 bash 主路径启用进程硬化，关闭时走原路径。"},
	{Key: "output_collapse", Env: "ARGENT_OUTPUT_COLLAPSE", Name: "bash 输出行折叠", Enable: "=1", Desc: "clipBashOutput 走有界的行折叠输出帧。"},
	{Key: "output_spill", Env: "ARGENT_OUTPUT_SPILL", Name: "超大输出落盘", Enable: "真值（1/true/yes/on）", Desc: "超大工具输出写入产物目录而非全部塞进上下文。"},
	{Key: "output_spill_dir", Env: "ARGENT_OUTPUT_SPILL_DIR", Name: "超大输出产物目录", Enable: "路径（默认 <storage>/artifacts）", Desc: "指定超大输出的落盘目录，位于存储目录而非工作区。"},
	{Key: "output_spill_threshold", Env: "ARGENT_OUTPUT_SPILL_THRESHOLD", Name: "超大输出字节阈值", Enable: "正整数", Desc: "覆盖超大输出的落盘阈值。"},
	{Key: "todo_strict", Env: "ARGENT_TODO_STRICT", Name: "todo 严格状态校验", Enable: "真值", Desc: "写入前用 ValidateTodoState 校验状态集，关闭时写路径逐字节不变。"},
	{Key: "cache_warm", Env: "ARGENT_CACHE_WARM", Name: "提示缓存保活", Enable: "=1", Desc: "开启提示缓存保活（cache warmer）。"},
	{Key: "loop_detection", Env: "ARGENT_LOOP_DETECTION", Name: "工具调用循环检测", Enable: "真值（1/true/yes/on）", Desc: "按调用签名检测重复循环，命中时注入纠正提示而非硬停（迁移自 killi repeat breaker）。"},
	{Key: "request_snapshot", Env: "ARGENT_REQUEST_SNAPSHOT", Name: "请求字节快照", Enable: "真值（1/true/yes/on）", Desc: "把实际发送的线协议字节追加到会话记录，用于复现审计；不参与对话视图，含完整提示需自行控制文件权限。"},
	{Key: "subagent_shared_prompt", Env: "ARGENT_SUBAGENT_SHARED_PROMPT", Name: "子代理共享父提示", Enable: "真值（1/true/yes/on）", Desc: "子代理以父级 system prompt 为前缀、角色约束追加在末尾，使首请求复用缓存；默认关闭时行为逐字节不变。"},
	{Key: "compact_drain", Env: "ARGENT_COMPACT_DRAIN", Name: "压缩后逐条淘汰", Enable: "真值（1/true/yes/on）", Desc: "上下文溢出压缩后仍超预算时，按缓存前缀逐条淘汰最旧非系统消息再重放；默认关闭时恢复路径逐字节不变。"},
	{Key: "resume_byte_replay", Env: "ARGENT_RESUME_BYTE_REPLAY", Name: "resume 字节回放", Enable: "真值（1/true/yes/on），且需同时开启 ARGENT_REQUEST_SNAPSHOT", Desc: "中断提示的 resume 用最后一条请求快照的原始线协议字节重发首请求，保持缓存前缀；协议/模型漂移或快照后有回复/压缩时自动回退。默认关闭。"},
	{Key: "acp_decode", Env: "ARGENT_ACP_DECODE", Name: "UTF-8→ACP 增量解码", Enable: "=1", Desc: "外部程序输出遇完整非法 UTF-8 序列时按当前代码页（Windows MultiByteToWideChar）解码并锁存到 EOF，按行判定避免污染相邻行；合法 UTF-8 直通零开销，默认关闭时逐字节不变。"},
	{Key: "hook_daemon", Env: "ARGENT_HOOK_DAEMON", Name: "钩子常驻进程扩展", Enable: "受信任钩子文档路径（非空即启用）", Desc: "通过 stdio JSON-RPC 挂载常驻外部进程，事件以虚拟钩子折入既有裁决/附加上下文管线；文档须过 SHA-256 信任门，崩溃自动降级。未设置时零文件 I/O、不派生进程。"},

	// ---- MCP / 产物 ----
	{Key: "mcp_image_artifact", Env: "ARGENT_MCP_IMAGE_ARTIFACT", Name: "MCP image 落盘", Enable: "真值", Desc: "把 image 内容块的 base64 写入产物目录，并在占位符附上可读路径。"},
	{Key: "mcp_media_blob_ext", Env: "ARGENT_MCP_MEDIA_BLOB_EXT", Name: "MCP 扩展媒体类型映射", Enable: "真值", Desc: "用更广的白名单 MIME→扩展名映射替代既有映射。"},
	{Key: "mcp_blob_text", Env: "ARGENT_MCP_BLOB_TEXT", Name: "MCP blob 当文本", Enable: "真值", Desc: "把解码后的 blob 按文本渲染，其余仍输出安全占位符。"},
	{Key: "mcp_blob_artifact_dir", Env: "ARGENT_MCP_BLOB_ARTIFACT_DIR", Name: "MCP blob 产物目录", Enable: "非空路径", Desc: "配置 blob/image 落盘目录；为空时落盘路径是空操作。"},
	{Key: "mcp_content_filter_min_priority", Env: "ARGENT_MCP_CONTENT_FILTER_MIN_PRIORITY", Name: "MCP 内容优先级过滤", Enable: "正整数", Desc: "按 audience/priority 过滤 content block，未设置时不过滤。"},
	{Key: "mcp_spill_dir", Env: "ARGENT_MCP_SPILL_DIR", Name: "MCP 输出溢出目录", Enable: "非空路径", Desc: "配置后 CallTool 的超大输出落盘，未配置时不溢出。"},
	{Key: "mcp_spill_threshold", Env: "ARGENT_MCP_SPILL_THRESHOLD", Name: "MCP 溢出阈值", Enable: "正整数", Desc: "覆盖 MCP 输出溢出的字节阈值。"},
	{Key: "mcp_protocol_version", Env: "ARGENT_MCP_PROTOCOL_VERSION", Name: "MCP 协议版本回退", Enable: "非空字符串", Desc: "协商失败时回退使用的 MCP 协议版本。"},
	{Key: "mcp_injection_scan", Env: "ARGENT_MCP_INJECTION_SCAN", Name: "MCP 工具输出注入扫描", Enable: "=1", Desc: "对 MCP CallTool 最终文本做提示注入检测，命中高风险时在原文前加一行可读警示；只警示不改写、不过滤原文任何字节；关闭时输出逐字节不变。"},
	{Key: "mcp_health", Env: "ARGENT_MCP_HEALTH", Name: "MCP 运行时健康诊断", Enable: "=1", Desc: "连接 MCP 服务时挂接 HealthTracker，关闭时把健康快照写入存储目录；doctor 读取后以 warn 级 issue 报出不健康服务的名称与 LastError；关闭时不挂接、不写文件、输出逐字节不变。"},

	// ---- 诊断 / 恢复 / 计时 ----
	{Key: "repeat_pitfall_tracking", Env: "ARGENT_REPEAT_PITFALL_TRACKING", Name: "跨情节重复踩坑计数", Enable: "=1", Desc: "启用跨情节的重复错误签名计数；关闭时 RecordSignature 立即返回，不分配不触锁，既有记录路径逐字节不变。"},
	{Key: "recovery_metrics", Env: "ARGENT_RECOVERY_METRICS", Name: "恢复成效账本", Enable: "=1", Desc: "在指引注入与工具成功处采集恢复成功率；关闭时不分配计数器，消息流逐字节不变。"},
	{Key: "turn_timing", Env: "ARGENT_TURN_TIMING", Name: "逐阶段计时日志", Enable: "=1", Desc: "agent 循环开启逐阶段结构化计时日志；关闭时计时器为 nil，日志与事件流逐字节不变。"},
	{Key: "stream_recovery", Env: "ARGENT_STREAM_RECOVERY", Name: "流中断非流式回补", Enable: "=1", Desc: "app 层接通 agent.Options.EnableStreamRecovery：提供方流中断且已输出部分文本时用同一请求非流式回补；关闭时该字段为零值，请求与事件逐字节不变。"},
	{Key: "tool_timing_histogram", Env: "ARGENT_TOOL_TIMING_HISTOGRAM", Name: "工具耗时直方图", Enable: "=1", Desc: "按工具名累积每次调用的精确耗时，轮末落 agent_tool_timing_histogram 摘要日志（TopSlow/P95）；关闭时字段为 nil，整段空操作。"},
	{Key: "tool_call_trace", Env: "ARGENT_TOOL_CALL_TRACE", Name: "工具调用链追踪", Enable: "=1", Desc: "为每次工具调用开/结算跨度，轮末落 agent_tool_call_trace 摘要日志（调用顺序、成败与错误文本）；关闭时字段为 nil，整段空操作。"},
	{Key: "config_reload_diff", Env: "ARGENT_CONFIG_RELOAD_DIFF", Name: "配置热重载差异摘要", Enable: "=1（精确匹配，不修剪空白）", Desc: "把热重载日志行从 reloaded: ... 改为基于键路径的中文差异摘要，敏感键显示 redacted 占位；关闭时日志逐字节不变。"},

	// ---- 评测 / 其它 ----
	{Key: "eval_live", Env: "ARGENT_EVAL_LIVE", Name: "评测 live 门禁", Enable: "=1", Desc: "允许评测走真实 provider；默认断网走脚本化。"},
}

// LookupIndependentOptIn 按环境变量名查找登记表；未登记返回 false。
func LookupIndependentOptIn(env string) (IndependentOptIn, bool) {
	for _, item := range IndependentOptIns {
		if item.Env == env {
			return item, true
		}
	}
	return IndependentOptIn{}, false
}
