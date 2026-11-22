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

	// ---- 上下文 / 压缩 / 会话 ----
	{Key: "continuation_brief", Env: "ARGENT_CONTINUATION_BRIEF", Name: "会话续接摘要", Enable: "=1", Desc: "resume/追问时向 system 提示注入上一段会话的有界摘要。"},
	{Key: "retain_guidance", Env: "ARGENT_RETAIN_GUIDANCE", Name: "纠正提示压缩存活", Enable: "=1", Desc: "保留纠正 nudge 记录的压缩存活标记，关闭时记录形状逐字节不变。"},
	{Key: "guidance_dedupe", Env: "ARGENT_GUIDANCE_DEDUPE", Name: "失败指引签名去重", Enable: "=1", Desc: "连续失败期间同一种错误签名只注入一次恢复指引，避免长对话堆积雷同指引；关闭时每轮照常注入，消息流逐字节不变。"},
	{Key: "summary_schema", Env: "ARGENT_SUMMARY_SCHEMA", Name: "压缩摘要固定 schema", Enable: "=1", Desc: "摘要产出后按固定顺序补齐 Goal/State/Next/Files 小节。"},
	{Key: "budget_projection", Env: "ARGENT_BUDGET_PROJECTION", Name: "消息预算投影", Enable: "=1", Desc: "Messages 前按 rune 预算投影，保护最新用户输入与未配对的活尾工具调用。"},
	{Key: "budget_projection_budget", Env: "ARGENT_BUDGET_PROJECTION_BUDGET", Name: "消息预算投影预算", Enable: "正整数（缺省 32000）", Desc: "指定投影的 rune 预算，非正或不可解析时回退保守默认。"},
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
	{Key: "builtin_shell", Env: "ARGENT_BUILTIN_SHELL", Name: "内建 shell 兜底", Enable: "=1", Desc: "切换到纯标准库自研 shell 解释器。"},
	{Key: "bash_use_proc", Env: "ARGENT_BASH_USE_PROC", Name: "bash 进程硬化", Enable: "=1", Desc: "默认 bash 主路径启用进程硬化，关闭时走原路径。"},
	{Key: "output_collapse", Env: "ARGENT_OUTPUT_COLLAPSE", Name: "bash 输出行折叠", Enable: "=1", Desc: "clipBashOutput 走有界的行折叠输出帧。"},
	{Key: "output_spill", Env: "ARGENT_OUTPUT_SPILL", Name: "超大输出落盘", Enable: "真值（1/true/yes/on）", Desc: "超大工具输出写入产物目录而非全部塞进上下文。"},
	{Key: "output_spill_dir", Env: "ARGENT_OUTPUT_SPILL_DIR", Name: "超大输出产物目录", Enable: "路径（默认 <storage>/artifacts）", Desc: "指定超大输出的落盘目录，位于存储目录而非工作区。"},
	{Key: "output_spill_threshold", Env: "ARGENT_OUTPUT_SPILL_THRESHOLD", Name: "超大输出字节阈值", Enable: "正整数", Desc: "覆盖超大输出的落盘阈值。"},
	{Key: "todo_strict", Env: "ARGENT_TODO_STRICT", Name: "todo 严格状态校验", Enable: "真值", Desc: "写入前用 ValidateTodoState 校验状态集，关闭时写路径逐字节不变。"},
	{Key: "cache_warm", Env: "ARGENT_CACHE_WARM", Name: "提示缓存保活", Enable: "=1", Desc: "开启提示缓存保活（cache warmer）。"},

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
