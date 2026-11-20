// Package i18n holds the user-facing string catalog for the CLI and TUI.
//
// The catalog is a Go table, not a set of locale files: the strings are part of
// the binary and cannot be lost at runtime. Two locales are supported, zh-CN
// and en-US. A missing key returns the key itself and is recorded, so a gap is
// visible in the UI and testable instead of rendering as blank.
package i18n

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Supported locale codes.
const (
	EnUS = "en-US"
	ZhCN = "zh-CN"
)

// DefaultLocale is used when nothing else matches.
const DefaultLocale = EnUS

type translation struct {
	en string
	zh string
}

// catalog is the embedded table. Keys are dotted, lower-case, grouped by the
// surface that owns them (prompt, status, label, approval, error, command,
// misc, info).
var catalog = map[string]translation{
	// Prompt area.
	"prompt.placeholder": {"Message argent…", "给 argent 发消息…"},
	"prompt.send":        {"send", "发送"},
	"prompt.abort":       {"abort the running turn", "中止当前回合"},
	"prompt.newline":     {"insert a line break", "插入换行"},
	"prompt.empty":       {"type a message, or / for commands", "输入消息，或输入 / 查看命令"},
	"prompt.queued":      {"queued follow-up", "后续消息已排队"},

	// Status labels.
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

	// Theme switching and colour degradation notices.
	"status.theme":          {"theme: %s", "主题：%s"},
	"notice.color_degraded": {"colour output degraded: NO_COLOR or dumb terminal", "颜色输出已降级：NO_COLOR 或 dumb 终端"},

	// Approval dialog.
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

	// Slash-command descriptions.
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

	// Misc UI.
	"misc.empty":       {"nothing to show", "暂无内容"},
	"misc.no_sessions": {"no sessions yet", "还没有会话"},
	"misc.bye":         {"bye", "再见"},
	"misc.press_enter": {"press Enter to continue", "按 Enter 继续"},

	// Informational lines.
	"info.skills_loaded":   {"%d skill(s) available", "可用技能 %d 个"},
	"info.commands_loaded": {"%d command(s) available", "可用命令 %d 个"},
	"info.memory_loaded":   {"%d memory file(s) loaded", "已加载 %d 个记忆文件"},
	"info.compacted":       {"compacted %d message(s)", "已压缩 %d 条消息"},

	// One-line activity recap of finished tool calls. The English tokens mirror
	// the neutral labels used by the TUI's tasks rail; the zh-CN forms are wider
	// in cells, so the rail clips by display width rather than rune count.
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

// Normalize returns a supported locale code, falling back to DefaultLocale.
func Normalize(locale string) string {
	if code, ok := Canonical(locale); ok {
		return code
	}
	return DefaultLocale
}

// Known returns the supported locale codes, sorted.
func Known() []string {
	return []string{EnUS, ZhCN}
}

// Keys returns every catalog key, sorted. Tests use it to check both locales
// are complete.
func Keys() []string {
	keys := make([]string, 0, len(catalog))
	for key := range catalog {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// T looks up one UI string.
//
// An unknown key is returned verbatim (so the gap is visible in the UI) and
// recorded for Missing. When args are supplied the template is formatted with
// fmt.Sprintf, e.g. T(locale, "error.model", "boom").
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

// Missing returns the keys (or key@locale pairs) requested but not found since
// the last ResetMissing, sorted and de-duplicated.
func Missing() []string {
	missingMu.Lock()
	defer missingMu.Unlock()
	out := make([]string, len(missingKeys))
	copy(out, missingKeys)
	return out
}

// ResetMissing clears the missing-key log.
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

// Detect picks a locale from the environment.
//
// ARGENT_LOCALE wins. Then LC_ALL, LC_MESSAGES and LANG are checked for a
// Chinese tag; anything else falls back to DefaultLocale. env must not be nil.
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
	}
	return DefaultLocale
}
