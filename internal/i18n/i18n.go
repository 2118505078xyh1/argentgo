// Package i18n 保存 CLI 和 TUI 的用户可见字符串目录。目录是 Go 表而非一组语言环境文件：字符串是二进制的一部分且不会在运行时丢失。支持两种语言环境：zh-CN 和 en-US。缺失的键返回键本身并被记录，使缺口在 UI 中可见且可测试，而非渲染为空白。支持的语言环境代码。DefaultLocale 在没有其他匹配时使用。
//
// 目录是 Go 表而非一组语言环境文件：字符串是二进制的
// 一部分且不会在运行时丢失。支持两种语言环境：zh-CN
// 和 en-US。缺失的键返回键本身并被记录，使缺口在 UI 中
// 可见且可测试，而非渲染为空白。
package i18n

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// 支持的语言环境代码。
const (
	EnUS = "en-US"
	ZhCN = "zh-CN"
)

// DefaultLocale 在没有其他匹配时使用。
const DefaultLocale = EnUS

type translation struct {
	en string
	zh string
}

// catalog 是嵌入的表。键以点分隔、小写，按拥有它们的
// 外观分组（prompt、status、label、approval、error、command、misc、info）。
var catalog = map[string]translation{
	// Prompt 区域。
	"prompt.placeholder": {"Message argentgo…", "给 argentgo 发消息…"},
	"prompt.send":        {"send", "发送"},
	"prompt.abort":       {"abort the running turn", "中止当前回合"},
	"prompt.newline":     {"insert a line break", "插入换行"},
	"prompt.empty":       {"type a message, or / for commands", "输入消息，或输入 / 查看命令"},
	"prompt.queued":      {"queued follow-up", "后续消息已排队"},
	// 可选的系统提示指令，要求模型用 UI 语言回复。
	// 保留在目录中使两种语言环境都可审计；
	// ReplyLanguageHint 解析语言环境并返回一侧。
	"prompt.reply_hint": {"Reply in English", "请用中文回复"},

	// 状态标签。
	"status.ready":    {"ready", "就绪"},
	"status.working":  {"working", "运行中"},
	"status.thinking": {"thinking", "思考中"},
	"status.done":     {"done", "完成"},
	"status.failed":   {"failed", "失败"},
	"status.aborted":  {"aborted", "已中止"},
	"status.retrying": {"retrying", "重试中"},
	"status.waiting":  {"waiting for approval", "等待确认"},

	// Labels.
	"label.model":      {"model", "模型"},
	"label.provider":   {"provider", "provider"},
	"label.thinking":   {"thinking", "思考"},
	"label.context":    {"context", "上下文"},
	"label.tokens":     {"tokens", "token"},
	"label.cost":       {"cost", "费用"},
	"label.session":    {"session", "会话"},
	"label.sessions":   {"sessions", "会话列表"},
	"label.tools":      {"tools", "工具"},
	"label.permission": {"permission", "权限"},
	"label.locale":     {"language", "语言"},
	"label.turns":      {"turns", "回合"},
	"label.theme":      {"theme", "主题"},

	// 主题切换和颜色降级通知。
	"status.theme":          {"theme: %s", "主题：%s"},
	"notice.color_degraded": {"colour output degraded: NO_COLOR or dumb terminal", "颜色输出已降级：NO_COLOR 或 dumb 终端"},

	// 审批对话框。
	"approval.title":     {"approval required", "需要确认"},
	"approval.question":  {"Allow this tool call? (%s)", "允许这次工具调用？(%s)"},
	"approval.allow":     {"allow once", "允许一次"},
	"approval.allow_all": {"allow for this session", "本会话内允许"},
	"approval.reject":    {"reject", "拒绝"},
	"approval.granted":   {"approved", "已允许"},
	"approval.denied":    {"rejected", "已拒绝"},
	"approval.always":    {"always allow", "始终允许"},
	"approval.never":     {"never ask (reject everything)", "从不询问（全部拒绝）"},
	"approval.prompt":    {"[y] allow  [a] always  [n] reject", "[y] 允许  [a] 始终允许  [n] 拒绝"},

	// Errors.
	"error.generic":      {"something went wrong", "出错了"},
	"error.model":        {"model error: %s", "模型错误：%s"},
	"error.network":      {"network error: %s", "网络错误：%s"},
	"error.timeout":      {"request timed out", "请求超时"},
	"error.canceled":     {"canceled", "已取消"},
	"error.not_found":    {"not found: %s", "未找到：%s"},
	"error.invalid_args": {"invalid arguments: %s", "参数无效：%s"},
	"error.permission":   {"permission denied", "没有权限"},
	"error.config":       {"config error: %s", "配置错误：%s"},
	"error.session":      {"cannot open session: %s", "无法打开会话：%s"},
	"error.tool":         {"tool failed: %s", "工具执行失败：%s"},
	"error.policy":       {"blocked by policy", "被策略拦截"},
	"error.retry":        {"press Enter to retry", "按 Enter 重试"},

	// 斜杠命令描述。
	"command.unknown":  {"unknown command: %s", "未知命令：%s"},
	"command.usage":    {"usage: %s", "用法：%s"},
	"command.help":     {"commands", "命令"},
	"command.clear":    {"conversation cleared", "会话已清空"},
	"command.compact":  {"context compacted", "上下文已压缩"},
	"command.resume":   {"open a stored session", "打开已有会话"},
	"command.quit":     {"quit", "退出"},
	"command.model":    {"switch model", "切换模型"},
	"command.init":     {"create AGENTS.md for this project", "为该项目创建 AGENTS.md"},
	"command.skills":   {"list installed skills", "列出已安装的技能"},
	"command.commands": {"list slash commands", "列出斜杠命令"},
	"command.memory":   {"show project memory files", "显示项目记忆文件"},

	// 其他 UI。
	"misc.empty":       {"nothing to show", "暂无内容"},
	"misc.no_sessions": {"no sessions yet", "还没有会话"},
	"misc.bye":         {"bye", "再见"},
	"misc.press_enter": {"press Enter to continue", "按 Enter 继续"},

	// 信息行。
	"info.skills_loaded":   {"%d skill(s) available", "可用技能 %d 个"},
	"info.commands_loaded": {"%d command(s) available", "可用命令 %d 个"},
	"info.memory_loaded":   {"%d memory file(s) loaded", "已加载 %d 个记忆文件"},
	"info.compacted":       {"compacted %d message(s)", "已压缩 %d 条消息"},

	// 已完成工具调用的单行活动回顾。英文标记镜像
	// TUI 任务栏使用的中性标签；zh-CN 形式在单元格上更宽，
	// 因此该栏按显示宽度而非 rune 数裁剪。
	"activity.command_one":  {"%d command", "%d 条命令"},
	"activity.command_many": {"%d commands", "%d 条命令"},
	"activity.edit_one":     {"%d edit", "%d 次编辑"},
	"activity.edit_many":    {"%d edits", "%d 次编辑"},
	"activity.read_one":     {"%d read", "%d 次读取"},
	"activity.read_many":    {"%d reads", "%d 次读取"},
	"activity.other_one":    {"%d other", "%d 项其他"},
	"activity.other_many":   {"%d others", "%d 项其他"},
	"activity.separator":    {" · ", " · "},

	// TUI 全屏界面的用户可见字符串（原 internal/tui/locale.go 双表迁入）。
	// 键名保留原样并加 tui. 前缀；文案逐字复制，标点和空格不可改动。
	"tui.allow":                {"allow", "允许"},
	"tui.allow_session":        {"allow for session", "本会话允许"},
	"tui.approval_title":       {"approval", "需要确认"},
	"tui.approved":             {"approved", "已允许"},
	"tui.approved_session":     {"approved for this session", "本会话已允许"},
	"tui.attempt_cancelled":    {"cancelled", "已取消"},
	"tui.attempt_failed":       {"failed", "失败"},
	"tui.attempt_retried":      {"retried", "已重试"},
	"tui.attempt_stream_error": {"stream error", "流式错误"},
	"tui.busy":                 {"a turn is still running", "当前回合尚未结束"},
	"tui.cached":               {"cached", "缓存"},
	"tui.cancelled":            {"cancelled", "已取消"},
	"tui.cancelling":           {"cancelling…", "正在取消…"},
	// 斜杠命令的一句话描述（补全浮层与命令面板共用）。原生命令与宿主
	// 内置命令共用一个目录，中文界面下不会混进英文短句。
	"tui.cmd_clear_desc":    {"clear the transcript", "清空对话记录"},
	"tui.cmd_compact_desc":  {"compact older context into a summary", "把较早的上下文压缩成摘要"},
	"tui.cmd_context_desc":  {"read-only view of the model context", "只读查看模型上下文"},
	"tui.cmd_expand_desc":   {"expand or collapse every reasoning block", "展开或收起全部思考块"},
	"tui.cmd_help_desc":     {"show all key bindings", "查看全部快捷键"},
	"tui.cmd_hotkeys_desc":  {"show all key bindings", "查看全部快捷键"},
	"tui.cmd_models_desc":   {"show the current model and how to change it", "查看当前模型及切换方式"},
	"tui.cmd_name_desc":     {"rename the current session", "给当前会话改名"},
	"tui.cmd_quit_desc":     {"leave the TUI", "退出 TUI"},
	"tui.cmd_resume_desc":   {"open the session list", "打开会话列表"},
	"tui.cmd_retry_desc":    {"fork the session at its latest checkpoint", "从最近检查点分叉重跑"},
	"tui.cmd_review_desc":   {"read-only review of the dirty git tree", "只读审查未提交的工作区改动"},
	"tui.cmd_sessions_desc": {"open the session list", "打开会话列表"},
	"tui.cmd_thinking_desc": {"show the current thinking level and how to change it", "查看当前思考强度及调整方式"},
	"tui.cmd_undo_desc":     {"revert the latest checkpoint on disk", "回滚最近一次检查点的磁盘改动"},
	"tui.collapsed":         {"tool output collapsed", "工具输出已折叠"},
	"tui.color_degraded":    {"colour output degraded (NO_COLOR or dumb terminal)", "颜色输出已降级（NO_COLOR 或 dumb 终端）"},
	"tui.commands_off":      {"commands panel hidden", "已隐藏命令面板"},
	"tui.commands_on":       {"commands panel shown (alt+c to hide)", "已显示命令面板（alt+c 隐藏）"},
	"tui.commands_title":    {"COMMANDS", "命令"},
	"tui.compact_done":      {"context compacted", "上下文已压缩"},
	"tui.compact_unwired":   {"/compact needs a session hook from the host", "/compact 需要宿主提供会话钩子"},
	"tui.copied":            {"copied to clipboard", "已复制到剪贴板"},
	"tui.copy_failed":       {"clipboard copy failed", "复制到剪贴板失败"},
	"tui.danger":            {"danger", "危险"},
	"tui.default_deny":      {"default: deny (enter refuses)", "默认：拒绝（回车即拒绝）"},
	"tui.denied":            {"denied", "已拒绝"},
	"tui.deny":              {"deny", "拒绝"},
	"tui.error":             {"error", "错误"},
	"tui.expanded":          {"tool output expanded", "工具输出已展开"},
	"tui.help":              {"enter sends · alt+enter/ctrl+j newline · ctrl+b/ctrl+f cursor · ctrl+a/ctrl+e line · ctrl+d exit on empty · ctrl+c cancels, again quits · ctrl+t thinking · ctrl+o tool output · ctrl+x copy · ctrl+g editor · ctrl+l/ctrl+p model · alt+t tasks · alt+b workspace · alt+p plan · alt+s sessions · alt+c commands · alt+u undo · alt+r retry · /clear /expand /undo /retry /name <title> /quit /help /sessions /models /thinking /compact /hotkeys (markdown commands expand $ARGUMENTS / $1..$N)", "enter 发送 · alt+enter/ctrl+j 换行 · ctrl+b/ctrl+f 移动 · ctrl+a/ctrl+e 行首尾 · ctrl+d 空行退出 · ctrl+c 取消，再按退出 · ctrl+t 思考块 · ctrl+o 工具输出 · ctrl+x 复制 · ctrl+g 外编辑器 · ctrl+l/ctrl+p 模型 · alt+t 任务 · alt+b 工作区 · alt+p 计划 · alt+s 会话 · alt+c 命令 · alt+u 撤销 · alt+r 重试 · /clear /expand /undo /retry /name <标题> /quit /help /sessions /models /thinking /compact /hotkeys（markdown 命令支持 $ARGUMENTS / $1..$N）"},
	"tui.hint":              {"enter send · alt+enter newline · pgup/pgdn scroll · ctrl+c cancel · /help", "enter 发送 · alt+enter 换行 · pgup/pgdn 滚动 · ctrl+c 取消 · /help"},
	"tui.hint_modal":        {"y allow · n deny · a allow for session · esc deny", "y 允许 · n 拒绝 · a 本次会话允许 · esc 拒绝"},
	"tui.models_info":       {"current model: %s · switch with argentgo --model <name>", "当前模型：%s · 切换请用 argentgo --model <模型>"},
	"tui.name_usage":        {"usage: /name <title>", "用法：/name <标题>"},
	"tui.named_off":         {"named-only filter off", "已关闭仅看命名会话"},
	"tui.named_on":          {"named-only filter on", "已开启仅看命名会话"},
	"tui.no_commands":       {"no commands yet", "暂无命令"},
	"tui.no_plan":           {"no plan submitted yet (submit_plan)", "尚未提交计划（submit_plan）"},
	"tui.no_sessions":       {"no sessions yet", "暂无会话"},
	"tui.nothing_to_copy":   {"no assistant message to copy", "没有可复制的助手消息"},
	"tui.no_tasks":          {"no tool calls yet", "暂无工具调用"},
	"tui.plan_off":          {"plan panel hidden", "已隐藏计划面板"},
	"tui.plan_on":           {"plan panel shown (alt+p to hide)", "已显示计划面板（alt+p 隐藏）"},
	"tui.plan_title":        {"PLAN", "计划"},
	"tui.ready":             {"ready", "就绪"},
	"tui.rename_done":       {"session renamed", "会话已重命名"},
	"tui.rename_unwired":    {"renaming needs a session hook from the host", "重命名需要宿主提供会话钩子"},
	"tui.retry_done":        {"retry requested", "已请求重试"},
	"tui.retry_unwired":     {"/retry needs a checkpoint hook from the host", "/retry 需要宿主提供检查点钩子"},
	"tui.sessions_off":      {"session rail hidden", "已隐藏会话列表"},
	"tui.sessions_on":       {"session rail shown (alt+s to hide)", "已显示会话列表（alt+s 隐藏）"},
	"tui.sessions_title":    {"SESSIONS", "会话"},
	"tui.sort_toggled":      {"session order reversed", "已反转会话顺序"},
	"tui.tasks_off":         {"tasks panel hidden", "已隐藏任务面板"},
	"tui.tasks_on":          {"tasks panel shown (alt+t to hide)", "已显示任务面板（alt+t 隐藏）"},
	"tui.tasks_title":       {"TASKS", "任务"},
	"tui.thinking_info":     {"current thinking: %s · change with argentgo --thinking <level>", "当前思考强度：%s · 调整请用 argentgo --thinking <档位>"},
	"tui.theme":             {"theme", "主题"},
	"tui.thinking":          {"thinking", "思考"},
	"tui.thinking_all_off":  {"all thinking blocks folded (ctrl+x to expand)", "已折叠全部思考块（ctrl+x 展开）"},
	"tui.thinking_all_on":   {"all thinking blocks expanded (ctrl+x to fold)", "已展开全部思考块（ctrl+x 折叠）"},
	"tui.tool":              {"tool", "工具"},
	"tui.undo_done":         {"undo finished", "撤销完成"},
	"tui.undo_unwired":      {"/undo needs a checkpoint hook from the host", "/undo 需要宿主提供检查点钩子"},
	"tui.working":           {"working…", "思考中…"},
	"tui.workspace_off":     {"workspace panel hidden", "已隐藏工作区面板"},
	"tui.workspace_on":      {"workspace panel shown (↑↓ move, ctrl+b to hide)", "已显示工作区面板（↑↓ 移动，ctrl+b 隐藏）"},
	"tui.workspace_title":   {"WORKSPACE", "工作区"},
}

var (
	missingMu   sync.Mutex
	missingSeen = map[string]bool{}
	missingKeys []string
)

// Canonical maps an alias such as "zh", "zh_CN" or "中文" to the catalog code.
func Canonical(locale string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(locale)) {
	case "en", "en-us", "en_us", "english":
		return EnUS, true
	case "zh", "zh-cn", "zh_cn", "chinese", "中文":
		return ZhCN, true
	}
	return "", false
}

// Normalize 返回支持的语言环境代码，回退到 DefaultLocale。
func Normalize(locale string) string {
	if code, ok := Canonical(locale); ok {
		return code
	}
	return DefaultLocale
}

// Known 返回支持的语言环境代码，已排序。
func Known() []string {
	return []string{EnUS, ZhCN}
}

// Normalize 返回支持的语言环境代码，回退到 DefaultLocale。Known 返回支持的语言环境代码，已排序。replyHintNeutral 在请求的语言环境不被此 UI 支持时返回。它是空字符串，使调用方完全不注入语言指令，而非向模型强加不支持或错误的语言。
// 支持。它是空字符串，使调用方完全不注入语言指令，
// 而非向模型强加不支持或错误的语言。
const replyHintNeutral = ""

// ReplyLanguageHint 返回调用方可追加到系统提示的
// 简短指令，要求模型用给定语言环境的语言回复。
//
// Locale aliases are resolved through Canonical, so "zh", "zh_cn" and "中文"
// ReplyLanguageHint 返回调用方可追加到系统提示的简短指令，要求模型用给定语言环境的语言回复。都产生中文提示，而 "en"、"en_us" 和 "English" 产生英文提示。不支持或空的语言环境产生 replyHintNeutral：不点名任何语言，因此可以安全省略提示，而不是选择用户未要求的默认值。
// 英文提示。不支持或空的语言环境产生 replyHintNeutral：不
// 点名任何语言，因此可以安全省略提示，而不是选择
// 用户未要求的默认值。
//
// 它是纯函数：不读取配置或全局状态，也不向
// Missing 记录任何内容。
func ReplyLanguageHint(locale string) string {
	code, ok := Canonical(locale)
	if !ok {
		return replyHintNeutral
	}
	return T(code, "prompt.reply_hint")
}

// outputLanguageTokenMaxLength 在验证前限制不可信偏好 token 的长度。长于此的散文绝不可能是语言代号。
// 验证前限制。长于此的散文绝不可能是语言代号。
const outputLanguageTokenMaxLength = 64

// bcp47Tag 是刻意严格的 BCP-47 形态：2-3 字母的主
// 子标签、可选的 4 字母脚本、可选的区域（2 字母或 3
// 数字），以及最多两种语法形式中的变体。两种字母
// 大小写都被显式写出而非使用 (?i)：RE2 的不区分大小写模式
// bcp47Tag 是刻意严格的 BCP-47 形态：2-3 字母的主子标签、可选的 4 字母脚本、可选的区域（2 字母或 3 数字），以及最多两种语法形式中的变体。两种字母大小写都被显式写出而非使用 (?i)：RE2 的不区分大小写模式会把 U+212A KELVIN SIGN 等易混淆字符折叠进 [a-z]，使仓库编写的 token 能冒充语言标签。
// 使仓库编写的 token 能冒充语言标签。
var bcp47Tag = regexp.MustCompile(`^[A-Za-z]{2,3}(?:-[A-Za-z]{4})?(?:-[A-Za-z]{2}|-[0-9]{3})?(?:-(?:[A-Za-z0-9]{5,8}|[0-9][A-Za-z0-9]{3,4})){0,2}$`)

// outputLanguageEntry 镜像产品的输出语言表。该表是
// 产品自身写入的闭集代号；仓库可写文件中的 token
// 仅当是这些拼写之一时才被信任
// outputLanguageEntry 镜像产品的输出语言表。该表是产品自身写入的闭集代号；仓库可写文件中的 token 仅当是这些拼写之一（代码、设置 id、完整英文名或本地名）时才被信任。
type outputLanguageEntry struct {
	code       string
	id         string
	fullName   string
	nativeName string
}

var outputLanguageTable = [...]outputLanguageEntry{
	{code: "en", id: EnUS, fullName: "English", nativeName: "English"},
	{code: "zh-TW", id: "zh-TW", fullName: "Traditional Chinese", nativeName: "繁體中文"},
	{code: "zh", id: ZhCN, fullName: "Chinese", nativeName: "中文"},
	{code: "ru", id: "ru-RU", fullName: "Russian", nativeName: "Русский"},
	{code: "de", id: "de-DE", fullName: "German", nativeName: "Deutsch"},
	{code: "ja", id: "ja-JP", fullName: "Japanese", nativeName: "日本語"},
	{code: "pt", id: "pt-BR", fullName: "Portuguese", nativeName: "Português"},
	{code: "fr", id: "fr-FR", fullName: "French", nativeName: "Français"},
	{code: "ca", id: "ca-ES", fullName: "Catalan", nativeName: "Català"},
}

// asciiLower 只折叠 ASCII 大写。strings.ToLower、strings.EqualFold
// asciiLower 只折叠 ASCII 大写。strings.ToLower、strings.EqualFold 和 RE2 的 (?i) 也会折叠非 ASCII 易混淆字符（如 U+212A KELVIN SIGN 到 "k"），使大小写伪装的 token 能混入闭集。已知的本地非 ASCII 名称也接受其小写拼写；只有不可信的 token 从不做 Unicode 折叠。
// 使大小写伪装的 token 能混入闭集。
func asciiLower(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			if out == nil {
				out = []byte(s)
			}
			out[i] = c + ('a' - 'A')
		}
	}
	if out == nil {
		return s
	}
	return string(out)
}

var outputLanguageDesignations = func() map[string]struct{} {
	set := make(map[string]struct{}, len(outputLanguageTable)*5)
	for _, entry := range outputLanguageTable {
		for _, spelling := range [...]string{entry.code, entry.id, entry.fullName, entry.nativeName} {
			if spelling == "" {
				continue
			}
			set[asciiLower(spelling)] = struct{}{}
			// 已知的本地非 ASCII 名称也接受其
			// 小写拼写；只有不可信的 token 从不做
			// Unicode 折叠。
			if lower := strings.ToLower(spelling); lower != spelling {
				set[lower] = struct{}{}
			}
		}
	}
	return set
}()

// ParseOutputLanguagePreference 验证来自不可信的、
// 仓库可写偏好文件的一个输出语言 token。
//
// 只接受 "auto"、来自输出语言表的代号（代码、设置
// id, full English name, or native name such as "中文" / "繁體中文"), or a
// 格式良好的 BCP-47 标签不被接受。一段 13 字符的 CJK
// 指令无需空格和标点，因此字符类无法把它
// 与语言名区分 —— 闭集可以。其他一切都被拒绝，
// 交给提示词。
//
// 这是纯函数：不读取配置，也不改变本包中任何现有
// 语言环境行为。
func ParseOutputLanguagePreference(raw string) (lang string, ok bool) {
	token := strings.TrimSpace(raw)
	if token == "" || len(token) > outputLanguageTokenMaxLength {
		return "", false
	}
	folded := asciiLower(token)
	if folded == "auto" {
		return "auto", true
	}
	if _, known := outputLanguageDesignations[folded]; known {
		return token, true
	}
	if bcp47Tag.MatchString(token) {
		return token, true
	}
	return "", false
}

// Keys 返回所有目录键，已排序。测试用它检查两种语言环境都完整。
// 都完整。
func Keys() []string {
	return sortedKeys(catalog)
}

func sortedKeys(cat map[string]translation) []string {
	keys := make([]string, 0, len(cat))
	for key := range cat {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// IssueKind 分类 AuditTranslations 报告的问题。
type IssueKind string

const (
	// IssueMissingText：某语言环境对目录仍定义的键的字符串
	// 为空，这是从未提供的翻译。
	IssueMissingText IssueKind = "missing_text"
	// 如 %s、%d）在语言环境之间不同，使某一语言环境渲染
	// "%!(EXTRA ...)" 或漏掉替换值。
	IssueFormatVerbMismatch IssueKind = "format_verb_mismatch"
	// IssuePlaceholderMismatch：Format 使用的命名 {name} 占位符
	// 多重集在语言环境之间不同，使某一语言环境留下占位符
	// 可见或忽略另一语言环境消费的值。
	IssuePlaceholderMismatch IssueKind = "placeholder_mismatch"
)

// Issue 是 AuditTranslations 发现的一条翻译完整性问题。所有字段都导出，使调用方（如 doctor 命令或测试）能检查和格式化它们而无需触及包内部。
// 所有字段都导出，使调用方（如 doctor 命令或测试）能
// 检查和格式化它们而无需触及包内部。
type Issue struct {
	Key    string    // catalog key with the problem
	Locale string    // locale code whose translation is deficient
	Kind   IssueKind // category of the problem
	Detail string    // short human-readable explanation
}

// AuditTranslations 检查跨语言环境的翻译完整性并返回发现的问题。它是纯函数：
// 只读取嵌入的目录，不修改任何状态（包括 Missing 日志），并返回按 Key、Locale、
// Kind 和 Detail 排序的发现。健康的目录返回空但非 nil 的切片。无参数时审计所有
// 支持的语言环境（Known）。参数通过 Canonical 规范化、去重并排序；不支持的
// 语言环境被忽略，因为目录只携带支持代码的文本。
//
// 除主目录（catalog）外，还审计四张独立消息表
// （command/reliability/opt-in/error-hint）：它们结构不同但同样持有 en/zh 文本，
// 统一转成 localeTexts 后复用同一套缺失文本/fmt 动词/命名占位符检查。
//
// 对每个键，第一个请求的语言环境中为 EnUS 的是参照（源字符串）。其他每个
// 请求的语言环境与它比较：
//
//   - 参照有文本但为空（IssueMissingText）；
//   - fmt 动词多重集不同（IssueFormatVerbMismatch）；
//   - 命名 {name} 占位符多重集不同（IssuePlaceholderMismatch）。
//
// 空参照本身作为该语言环境的 IssueMissingText 报告。各表内所有语言环境共享
// 键集，因此一个键不可能在某个语言环境中"多余"；有意义的遗漏是空文本和
// 丢失的动词/占位符，这正是本审计检测的。
func AuditTranslations(locales ...string) []Issue {
	issues := auditCatalog(catalog, locales)
	for _, table := range messageTableViews() {
		issues = append(issues, auditLocaleTexts(table.entries, locales)...)
	}
	sortIssues(issues)
	return issues
}

// auditCatalog 是 AuditTranslations 在主目录上的入口，保留原签名供测试直接使用。
func auditCatalog(cat map[string]translation, locales []string) []Issue {
	view := make(map[string]localeTexts, len(cat))
	for key, entry := range cat {
		view[key] = localeTexts{en: entry.en, zh: entry.zh}
	}
	return auditLocaleTexts(view, locales)
}

// auditLocaleTexts 对任意 en/zh 文案表做跨语言完整性检查：缺失文本、fmt 动词
// 不一致与命名占位符不一致。逻辑与原先的 auditCatalog 逐字等价，仅把 entry
// 类型从 translation 换成统一的 localeTexts，使四张独立消息表也能复用。
func auditLocaleTexts(cat map[string]localeTexts, locales []string) []Issue {
	requested := normalizeAuditLocales(locales)
	if len(requested) == 0 {
		return []Issue{}
	}
	reference := referenceLocale(requested)

	issues := make([]Issue, 0)
	keys := make([]string, 0, len(cat))
	for key := range cat {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry := cat[key]
		for _, loc := range requested {
			if textForLocale(loc, entry) == "" {
				issues = append(issues, Issue{
					Key: key, Locale: loc, Kind: IssueMissingText,
					Detail: "empty translation",
				})
			}
		}

		ref := textForLocale(reference, entry)
		if ref == "" {
			continue // nothing to compare against; emptiness already reported
		}
		refVerbs, refNames := formatVerbs(ref), namedPlaceholders(ref)
		for _, loc := range requested {
			if loc == reference {
				continue
			}
			text := textForLocale(loc, entry)
			if text == "" {
				continue // already reported as IssueMissingText
			}
			if verbs := formatVerbs(text); !sameCounts(refVerbs, verbs) {
				issues = append(issues, Issue{
					Key: key, Locale: loc, Kind: IssueFormatVerbMismatch,
					Detail: "format verbs " + describeCounts(verbs) + " vs reference " + describeCounts(refVerbs),
				})
			}
			if names := namedPlaceholders(text); !sameCounts(refNames, names) {
				issues = append(issues, Issue{
					Key: key, Locale: loc, Kind: IssuePlaceholderMismatch,
					Detail: "placeholders " + describeCounts(names) + " vs reference " + describeCounts(refNames),
				})
			}
		}
	}

	sortIssues(issues)
	return issues
}

// sortIssues 按 (Key, Locale, Kind, Detail) 稳定排序一份 Issue 列表。
func sortIssues(issues []Issue) {
	sort.Slice(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		if a.Locale != b.Locale {
			return a.Locale < b.Locale
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Detail < b.Detail
	})
}

// normalizeAuditLocales 规范化调用方输入。空请求表示"所有支持的语言环境"。未知语言环境被丢弃。
// "所有支持的语言环境"。未知语言环境被丢弃。
func normalizeAuditLocales(locales []string) []string {
	if len(locales) == 0 {
		return Known()
	}
	seen := map[string]bool{}
	var out []string
	for _, locale := range locales {
		code, ok := Canonical(locale)
		if !ok || (code != EnUS && code != ZhCN) || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// referenceLocale 挑选其文本作为比较来源的语言环境。
// 优先英语；否则使用第一个（排序后的）语言环境。
func referenceLocale(locales []string) string {
	for _, locale := range locales {
		if locale == EnUS {
			return EnUS
		}
	}
	return locales[0]
}

// textForLocale 从统一的 en/zh 条目中选择某语言环境的字符串。未知代码回退到
// 英语，匹配 T 的默认语言环境行为；对独立消息表与 catalog 一视同仁。
func textForLocale(locale string, e localeTexts) string {
	if locale == ZhCN {
		return e.zh
	}
	return e.en
}

// isASCIILetter 报告 c 是否为 ASCII 字母。
func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isFmtDirectiveByte 报告 c 是否可出现在 '%' 与
// 转换动词之间：标志、宽度、精度、参数索引及其填充。
func isFmtDirectiveByte(c byte) bool {
	switch c {
	case '+', '-', '#', '0', ' ', '.', '[', ']':
		return true
	}
	return c >= '0' && c <= '9'
}

// isASCIILetter 报告 c 是否为 ASCII 字母。isFmtDirectiveByte 报告 c 是否可出现在 '%' 与转换动词之间：标志、宽度、精度、参数索引及其填充。formatVerbs 返回 s 中每个 fmt.Sprintf 转换动词的计数。动词是终止 '%' 指令的 ASCII 字母；标志、宽度、精度和参数索引不改变它。"%%" 是字面百分号且不贡献动词。畸形或末尾的 '%' 被忽略。
// 动词是终止 '%' 指令的 ASCII 字母；标志、宽度、
// 精度和参数索引不改变它。"%%" 是字面百分号且
// 不贡献动词。畸形或末尾的 '%' 被忽略。
func formatVerbs(s string) map[string]int {
	out := map[string]int{}
	for i := 0; i < len(s); {
		if s[i] != '%' {
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '%' {
			i += 2
			continue
		}
		j := i + 1
		for j < len(s) && isFmtDirectiveByte(s[j]) {
			j++
		}
		if j < len(s) && isASCIILetter(s[j]) {
			out[string(s[j])]++
			i = j + 1
			continue
		}
		i++ // stray '%': treat as literal
	}
	return out
}

// namedPlaceholders 返回 s 中每个 {name} 占位符的计数，使用
// 与 Format 相同的扫描规则（转义的大括号不是占位符）。
func namedPlaceholders(s string) map[string]int {
	out := map[string]int{}
	for i := 0; i < len(s); {
		if s[i] == '{' {
			if i+1 < len(s) && s[i+1] == '{' {
				i += 2
				continue
			}
			if name, end, ok := placeholderAt(s, i); ok {
				out[name]++
				i = end
				continue
			}
		}
		i++
	}
	return out
}

func sameCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// describeCounts 为 Issue.Detail 确定性地渲染计数 map。
func describeCounts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if m[k] == 1 {
			parts = append(parts, k)
		} else {
			parts = append(parts, fmt.Sprintf("%s*%d", k, m[k]))
		}
	}
	return strings.Join(parts, " ")
}

// T 查找一个 UI 字符串。
//
// 未知键被原样返回（使缺口在 UI 中可见）并
// 记录到 Missing。提供 args 时用 fmt.Sprintf
func T(locale, key string, args ...any) string {
	entry, ok := catalog[key]
	if !ok {
		recordMissing(key)
		return key
	}
	code := Normalize(locale)
	template := entry.en
	if code == ZhCN {
		template = entry.zh
	}
	if template == "" {
		recordMissing(key + "@" + code)
		template = entry.en
	}
	if len(args) > 0 {
		return fmt.Sprintf(template, args...)
	}
	return template
}

// placeholderNameStart 报告 c 是否可以开始占位符名。
// 名称刻意只含 ASCII，使展开确定且不能
// placeholderNameStart 报告 c 是否可以开始占位符名。名称刻意只含 ASCII，使展开确定且不能依赖 Unicode 大小写折叠；CJK 字节（>= 0x80）永不匹配，因此被原样复制通过。
// 因此被原样复制通过。
func placeholderNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func placeholderNameByte(c byte) bool {
	return placeholderNameStart(c) || (c >= '0' && c <= '9')
}

// placeholderAt 报告从 text[start] 开始的 {name} 占位符的名称
// 及其刚越过的索引（若存在）。只有后跟 '}' 的完整标识符
// placeholderAt 报告从 text[start] 开始的 {name} 占位符的名称及其刚越过的索引（若存在）。只有后跟 '}' 的完整标识符才算，因此 {names} 或 {a-b} 等相邻文本不会被误认为不同的占位符。
// 被误认为不同的占位符。
func placeholderAt(text string, start int) (name string, end int, ok bool) {
	i := start + 1
	if i >= len(text) || !placeholderNameStart(text[i]) {
		return "", 0, false
	}
	nameStart := i
	for i < len(text) && placeholderNameByte(text[i]) {
		i++
	}
	if i >= len(text) || text[i] != '}' {
		return "", 0, false
	}
	return text[nameStart:i], i + 1, true
}

// Format 展开 text 中的命名 {name} 占位符。text 通常是通过
// T（解析语言环境）从嵌入目录获得的字符串；
// Format 本身是纯的且与语言环境无关。它用可重排、自描述的
// 占位符补充 T 的位置式 fmt.Sprintf 格式化。
//
// 展开规则：
//
//	  它被替换为 vars[name]。
//	- 名称不在 vars 中的占位符原样保留，使
//	  缺失的翻译变量可见而非静默变空。
//	- {{ 和 }} 发出字面大括号；{{name}} 渲染为 {name} 且
//	  不被展开（转义）。
//	- 只展开完整的 {identifier}，因此 {names} 永不与
//	  {name} 混淆且占位符相邻的文本被保留。
//	- 多字节文本（CJK 及其他）逐字节复制通过。
//	- 不开始有效占位符的大括号（如 {a-b}、{9x}、未闭合的
//	  '{'）被原样复制；嵌套大括号被扫描使内部的
//	  有效占位符仍能展开。
//	- vars 中的值被直接写入且从不重扫，因此含
//	  大括号的值无法注入更多占位符。
//
// 空但非 nil 的 map；nil vars map 有效，此时每个占位符保持原样。
func Format(text string, vars map[string]string) string {
	if !strings.Contains(text, "{") {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		c := text[i]
		if c == '{' {
			if i+1 < len(text) && text[i+1] == '{' {
				b.WriteByte('{')
				i += 2
				continue
			}
			if name, end, ok := placeholderAt(text, i); ok {
				if value, found := vars[name]; found {
					b.WriteString(value)
				} else {
					b.WriteString(text[i:end])
				}
				i = end
				continue
			}
		}
		if c == '}' && i+1 < len(text) && text[i+1] == '}' {
			b.WriteByte('}')
			i += 2
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// Missing 返回自上次 ResetMissing 以来被请求但未找到的键（或 key@locale 对），已排序去重。ResetMissing 清除缺失键日志。
// 自上次 ResetMissing 以来被请求但未找到的键（或 key@locale 对），已排序去重。
func Missing() []string {
	missingMu.Lock()
	defer missingMu.Unlock()
	out := make([]string, len(missingKeys))
	copy(out, missingKeys)
	return out
}

// ResetMissing 清除缺失键日志。
func ResetMissing() {
	missingMu.Lock()
	defer missingMu.Unlock()
	missingSeen = map[string]bool{}
	missingKeys = nil
}

func recordMissing(id string) {
	missingMu.Lock()
	defer missingMu.Unlock()
	if missingSeen[id] {
		return
	}
	missingSeen[id] = true
	missingKeys = append(missingKeys, id)
	sort.Strings(missingKeys)
}

// isExplicitEnglishLocale 报告环境语言环境值是否是对英语的
// 显式偏好，包括真实环境附加的后缀：区域
// （"en-GB"、"en_US"）、编码（"en_US.UTF-8"、
// 就 UI 而言是英语。
//
// Detect 按前缀识别中文，因此没有这个检查时，优先级更高的
// 显式英语语言环境变量看起来只是"无法识别"而
// 被跳过；随后优先级更低的中文 LANG 会静默覆盖
// 用户自己的设置。只有英语被特殊处理：其他不支持的
// 语言仍照旧穿透。
func isExplicitEnglishLocale(value string) bool {
	lower := asciiLower(strings.TrimSpace(value))
	// POSIX 编码和修饰符后缀跟在语言标签后且
	// 不改变请求的是哪种语言。
	if i := strings.IndexAny(lower, ".@"); i >= 0 {
		lower = lower[:i]
	}
	switch lower {
	case "c", "posix":
		return true
	}
	if i := strings.IndexAny(lower, "-_"); i >= 0 {
		lower = lower[:i]
	}
	return lower == "en" || lower == "english"
}

// Detect 从环境中选择语言环境。
//
// Detect 从环境中选择语言环境。ARGENT_LOCALE 优先。然后检查 LC_ALL、LC_MESSAGES 和 LANG：中文标签选择 ZhCN，显式英语标签（包括 "C"/"POSIX" 以及带区域、编码或修饰符后缀的值）选择 DefaultLocale 并停止搜索。其他任何值穿透到下一个变量。env 不得为 nil。
// 中文标签选择 ZhCN，显式英语标签（包括 "C"/"POSIX" 以及带
// 区域、编码或修饰符后缀的值）选择 DefaultLocale 并停止
// 搜索。其他任何值穿透到下一个变量。env 不得
// 为 nil。
func Detect(env func(string) string) string {
	if env == nil {
		return DefaultLocale
	}
	for _, name := range []string{"ARGENT_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		value := strings.TrimSpace(env(name))
		if value == "" {
			continue
		}
		if code, ok := Canonical(value); ok {
			return code
		}
		lower := strings.ToLower(value)
		if strings.HasPrefix(lower, "zh") {
			return ZhCN
		}
		if isExplicitEnglishLocale(value) {
			return DefaultLocale
		}
	}
	return DefaultLocale
}
