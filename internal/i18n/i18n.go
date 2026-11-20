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
	"prompt.placeholder": {"Message argent…", "给 argent 发消息…"},
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

// AuditTranslations 检查跨语言环境的翻译完整性并返回
// 发现的问题。它是纯函数：只读取嵌入的
// 目录，不修改任何状态（包括 Missing 日志），并返回
// AuditTranslations 检查跨语言环境的翻译完整性并返回发现的问题。它是纯函数：只读取嵌入的目录，不修改任何状态（包括 Missing 日志），并返回按 Key、Locale、Kind 和 Detail 排序的发现。健康的目录返回空但非 nil 的切片。无参数时审计所有支持的语言环境（Known）。参数通过 Canonical 规范化、去重并排序；不支持的语言环境被忽略，因为目录只携带支持代码的文本。
// 非 nil 切片。
//
// 无参数时审计所有支持的语言环境（Known）。参数通过
// Canonical 规范化、去重并排序；不支持的语言环境
// 被忽略，因为目录只携带支持代码的文本。
//
// 对每个键，第一个请求的语言环境中为 EnUS 的是参照（
// 源字符串）。其他每个请求的语言环境与它比较：
//   - 参照有文本但为空（IssueMissingText）；
//
//   - 命名 {name} 占位符多重集不同
//     (IssuePlaceholderMismatch).
//
// 空参照本身作为该语言环境的 IssueMissingText 报告。
// 此目录中所有语言环境共享键集，因此一个键不可能
// 在某个语言环境中"多余"；有意义的遗漏是空文本和丢失的
// 占位符，这正是本审计检测的。
func AuditTranslations(locales ...string) []Issue {
	return auditCatalog(catalog, locales)
}

func auditCatalog(cat map[string]translation, locales []string) []Issue {
	requested := normalizeAuditLocales(locales)
	if len(requested) == 0 {
		return []Issue{}
	}
	reference := referenceLocale(requested)

	issues := make([]Issue, 0)
	for _, key := range sortedKeys(cat) {
		entry := cat[key]
		for _, loc := range requested {
			if textFor(loc, entry) == "" {
				issues = append(issues, Issue{
					Key: key, Locale: loc, Kind: IssueMissingText,
					Detail: "empty translation",
				})
			}
		}

		ref := textFor(reference, entry)
		if ref == "" {
			continue // nothing to compare against; emptiness already reported
		}
		refVerbs, refNames := formatVerbs(ref), namedPlaceholders(ref)
		for _, loc := range requested {
			if loc == reference {
				continue
			}
			text := textFor(loc, entry)
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
	return issues
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

// textFor 从条目中选择某语言环境的字符串。未知代码回退到
// referenceLocale 挑选其文本作为比较来源的语言环境。优先英语；否则使用第一个（排序后的）语言环境。textFor 从条目中选择某语言环境的字符串。未知代码回退到英语，匹配 T 的默认语言环境行为。
func textFor(locale string, e translation) string {
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
