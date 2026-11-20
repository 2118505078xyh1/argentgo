// Package i18n holds the user-facing string catalog for the CLI and TUI.
//
// The catalog is a Go table, not a set of locale files: the strings are part of
// the binary and cannot be lost at runtime. Two locales are supported, zh-CN
// and en-US. A missing key returns the key itself and is recorded, so a gap is
// visible in the UI and testable instead of rendering as blank.
package i18n

import (
	"fmt"
	"regexp"
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

// outputLanguageTokenMaxLength bounds an untrusted preference token before it
// is validated. Prose longer than this can never be a language designation.
const outputLanguageTokenMaxLength = 64

// bcp47Tag is a deliberately strict BCP-47 shape: a 2-3 letter primary
// subtag, an optional 4-letter script, an optional region (2 letters or 3
// digits), and up to two variants in the grammar's two forms. Both letter
// cases are spelled out instead of using (?i): RE2's case-insensitive mode
// folds confusables such as U+212A KELVIN SIGN into [a-z], which would let a
// repository-authored token pass as a language tag.
var bcp47Tag = regexp.MustCompile(`^[A-Za-z]{2,3}(?:-[A-Za-z]{4})?(?:-[A-Za-z]{2}|-[0-9]{3})?(?:-(?:[A-Za-z0-9]{5,8}|[0-9][A-Za-z0-9]{3,4})){0,2}$`)

// outputLanguageEntry mirrors the product's output-language table. The table
// is the closed set of designations the product itself writes; a token found
// in a repository-writable file is trusted only when it is one of these
// spellings (code, settings id, full English name, or native name).
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

// asciiLower folds ASCII upper case only. strings.ToLower, strings.EqualFold
// and RE2's (?i) also fold non-ASCII confusables (e.g. U+212A KELVIN SIGN to
// "k"), which would let a case-disguised token slip into the closed set.
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
			// Known non-ASCII native names are also accepted in their
			// lower-cased spelling; only the untrusted token is never
			// Unicode-folded.
			if lower := strings.ToLower(spelling); lower != spelling {
				set[lower] = struct{}{}
			}
		}
	}
	return set
}()

// ParseOutputLanguagePreference validates one output-language token that came
// from an untrusted, repository-writable preference file.
//
// Only "auto", a designation from the output-language table (code, settings
// id, full English name, or native name such as "中文" / "繁體中文"), or a
// well-formed BCP-47 tag is accepted. A 13-character instruction written in
// CJK needs no spaces and no punctuation, so a character class cannot tell it
// apart from a language name — the closed set can. Everything else is
// rejected with ok == false and an empty lang; callers must never hand the raw
// string to a prompt.
//
// This is a pure function. It reads no configuration and changes no existing
// locale behaviour in this package.
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

// Keys returns every catalog key, sorted. Tests use it to check both locales
// are complete.
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

// IssueKind classifies a problem reported by AuditTranslations.
type IssueKind string

const (
	// IssueMissingText: a locale's string is empty for a key that the catalog
	// still defines. This is a translation that was never provided.
	IssueMissingText IssueKind = "missing_text"
	// IssueFormatVerbMismatch: the multiset of fmt.Sprintf conversion verbs
	// (e.g. %s, %d) differs between locales, so one locale will render
	// "%!(EXTRA ...)" or miss a substituted value.
	IssueFormatVerbMismatch IssueKind = "format_verb_mismatch"
	// IssuePlaceholderMismatch: the multiset of named {name} placeholders used
	// by Format differs between locales, so one locale leaves a placeholder
	// visible or ignores a value the other consumes.
	IssuePlaceholderMismatch IssueKind = "placeholder_mismatch"
)

// Issue is one translation-completeness problem found by AuditTranslations.
// All fields are exported so callers (e.g. a doctor command or a test) can
// inspect and format them without reaching into package internals.
type Issue struct {
	Key    string    // catalog key with the problem
	Locale string    // locale code whose translation is deficient
	Kind   IssueKind // category of the problem
	Detail string    // short human-readable explanation
}

// AuditTranslations checks translation completeness across locales and returns
// the problems found. It is a pure function: it reads only the embedded
// catalog, mutates no state (including the Missing log), and returns issues
// sorted by Key, Locale, Kind and Detail. A healthy catalog returns an empty,
// non-nil slice.
//
// With no arguments every supported locale (Known) is audited. Arguments are
// canonicalised via Canonical, de-duplicated and sorted; unsupported locales
// are ignored because the catalog only carries text for the supported codes.
//
// For each key, the first requested locale that is EnUS is the reference (the
// source strings). Every other requested locale is compared against it for:
//   - empty text where the reference has text (IssueMissingText);
//   - a differing multiset of fmt.Sprintf verbs (IssueFormatVerbMismatch);
//   - a differing multiset of named {name} placeholders
//     (IssuePlaceholderMismatch).
//
// An empty reference is itself reported as IssueMissingText for that locale.
// The key set is shared by all locales in this catalog, so a key cannot be
// "extra" in one locale; the meaningful omissions are empty text and dropped
// placeholders, which is what this audit detects.
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

// normalizeAuditLocales canonicalises caller input. An empty request means
// "all supported locales". Unknown locales are dropped.
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

// referenceLocale picks the locale whose text is the source for comparisons.
// English is preferred; otherwise the first (sorted) locale is used.
func referenceLocale(locales []string) string {
	for _, locale := range locales {
		if locale == EnUS {
			return EnUS
		}
	}
	return locales[0]
}

// textFor selects a locale's string from an entry. Unknown codes fall back to
// English, matching T's default-locale behaviour.
func textFor(locale string, e translation) string {
	if locale == ZhCN {
		return e.zh
	}
	return e.en
}

// isASCIILetter reports whether c is an ASCII letter.
func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isFmtDirectiveByte reports whether c may appear between '%' and a
// conversion verb: flags, width, precision, argument index and their padding.
func isFmtDirectiveByte(c byte) bool {
	switch c {
	case '+', '-', '#', '0', ' ', '.', '[', ']':
		return true
	}
	return c >= '0' && c <= '9'
}

// formatVerbs returns a count of each fmt.Sprintf conversion verb in s. A
// verb is the ASCII letter that terminates a '%' directive; flags, width,
// precision and argument indexes do not change it. "%%" is a literal percent
// and contributes no verb. A malformed or trailing '%' is ignored.
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

// namedPlaceholders returns a count of each {name} placeholder in s, using
// the same scanning rules as Format (escaped braces are not placeholders).
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

// describeCounts renders a count map deterministically for Issue.Detail.
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

// placeholderNameStart reports whether c may begin a placeholder name.
// Names are deliberately ASCII-only so expansion is deterministic and cannot
// depend on Unicode case folding; CJK bytes (>= 0x80) never match and are
// therefore copied through untouched.
func placeholderNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func placeholderNameByte(c byte) bool {
	return placeholderNameStart(c) || (c >= '0' && c <= '9')
}

// placeholderAt reports the name and the index just past a {name} placeholder
// beginning at text[start], if one is present. Only a complete identifier
// followed by '}' counts, so adjacent text such as {names} or {a-b} is not
// mistaken for a different placeholder.
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

// Format expands named {name} placeholders in text. The text is normally a
// string obtained from the embedded catalog via T (which resolves the locale);
// Format itself is pure and locale-agnostic. It complements T's positional
// fmt.Sprintf formatting with reorderable, self-describing placeholders.
//
// Expansion rules:
//   - A placeholder is {name} where name matches [A-Za-z_][A-Za-z0-9_]*.
//     It is replaced with vars[name].
//   - A placeholder whose name is absent from vars is left verbatim, so a
//     missing translation variable is visible rather than silently blank.
//   - {{ and }} emit a literal brace; {{name}} thus renders as {name} and is
//     not expanded (escaping).
//   - Only a complete {identifier} is expanded, so {names} is never confused
//     with {name} and text adjacent to a placeholder is preserved.
//   - Multi-byte text (CJK and others) is copied through byte-for-byte.
//   - A brace that does not begin a valid placeholder (e.g. {a-b}, {9x}, an
//     unclosed '{') is copied verbatim; nested braces are scanned so an inner
//     valid placeholder still expands.
//   - Values from vars are written directly and never re-scanned, so a value
//     containing braces cannot inject further placeholders.
//
// A nil vars map is valid: every placeholder is then left intact.
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

// isExplicitEnglishLocale reports whether an environment locale value is an
// explicit preference for English, including the suffixes real environments
// attach to it: a region ("en-GB", "en_US"), an encoding ("en_US.UTF-8",
// "C.UTF-8") and a modifier ("en_US@euro"). C and POSIX are the C locale,
// which is English for UI purposes.
//
// Detect recognises Chinese by prefix, so without this check a higher-priority
// variable holding an explicit English locale looks merely "unrecognised" and
// is skipped; a lower-priority Chinese LANG then silently overrides the
// user's own setting. Only English is special-cased: other unsupported
// languages still fall through, as before.
func isExplicitEnglishLocale(value string) bool {
	lower := asciiLower(strings.TrimSpace(value))
	// POSIX encoding and modifier suffixes follow the language tag and do not
	// change which language was requested.
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

// Detect picks a locale from the environment.
//
// ARGENT_LOCALE wins. Then LC_ALL, LC_MESSAGES and LANG are checked: a Chinese
// tag selects ZhCN, an explicit English tag (including "C"/"POSIX" and values
// with region, encoding or modifier suffixes) selects DefaultLocale and stops
// the search. Anything else falls through to the next variable. env must not
// be nil.
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
