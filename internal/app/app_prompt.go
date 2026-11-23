package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/i18n"
	"github.com/2118505078xyh1/argentgo/internal/memory"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/skills"
)

// buildSystem 为现有调用方（尤其是 Resolve）构建系统提示。项目指令一直是无条件注入的，
// 因此这个包装器保留该旧版默认行为：除非调用方通过 ARGENT_TRUST_PROJECT 显式选择项目边界，
// 否则 AGENTS.md/CLAUDE.md 仍会被注入（可识别的假值见 parseTrustEnv）。
// 感知信任的实现位于 buildSystemWithTrust；完整语义见该函数。
// 可选的 verify 参数携带已解析的验证配置，使提示能告诉模型编辑后已经运行了哪些检查。
// 它之所以是可变参数，纯粹是为了保持众多旧调用点的字节兼容性：不传任何参数的调用方
// 会得到 config.Config{}（功能关闭）。
func buildSystem(root string, env func(string) string, thinking, schema string, verify ...config.Config) string {
	return buildSystemWithTrust(root, env, trustFromEnv(env), thinking, schema, verify...)
}

// trustFromEnv 为旧版入口推导项目指令的信任决定。它委托给 parseTrustEnv，
// 使提示路径与 Resolve 的 Resolved.TrustProject 决定对 ARGENT_TRUST_PROJECT 共享同一解释：
// 0/false/no/off 表示不信任，1/true/yes/on 表示信任，未设置时默认信任，
// 以保留历史上"总是注入 AGENTS.md"的行为。
func trustFromEnv(env func(string) string) bool {
	if env == nil {
		return true
	}
	return parseTrustEnv(env("ARGENT_TRUST_PROJECT"))
}

// buildStaticSystem 构建系统提示中缓存稳定的部分：角色设定、工具使用指引、项目指令和技能索引。
// 它刻意排除所有与进程相关的值（cwd、推理强度、输出 schema），
// 使会话移动到不同目录或调整思考级别时，提供方缓存的字节不会改变。
// 把这些值作为用户角色消息注入的调用方，应将其与 buildDynamicContext 配对使用；
// buildSystemWithTrust 为旧版调用方组合两者。
func buildStaticSystem(root string, env func(string) string, trust bool, verify ...config.Config) string {
	var b strings.Builder
	b.WriteString("You are argentgo, a concise coding assistant working on the user's machine.\n")
	b.WriteString("Use the available tools to inspect and change files. Read before writing, ")
	b.WriteString("batch independent calls, and stop once the request is satisfied.\n")

	// 平台事实是进程级的（运行时常量 + 环境变量 + 工作区根目录的一次 stat），
	// 因此它们属于缓存稳定的前缀。验证相关的尾部只有在操作人员确实配置了检查时才会出现。
	if facts := platformFacts(root, env, firstVerify(verify)); facts != "" {
		b.WriteString("\n")
		b.WriteString(facts)
		b.WriteString("\n")
	}

	if docs := projectMemoryDocs(root, env, trust); len(docs) > 0 {
		if block := memory.Block(docs); strings.TrimSpace(block) != "" {
			b.WriteString("\n")
			b.WriteString(block)
		}
	}
	// 技能通过共享缓存发现；这里只用到注册表，因此传入 nil 的命令根目录。
	// 指纹会在磁盘变化时重新发现，因此重复构建提示会折叠为仅一次 stat 的命中。
	_, registry := sharedDefs().Get(nil, skills.DefaultRoots(root, homeDir(env)))
	if registry.Len() > 0 {
		b.WriteString("\n")
		b.WriteString(registry.IndexText())
	}
	// 回复语言是进程级的偏好，因此其指令与角色设定一起放在缓存稳定的前缀中。
	// 默认/英文设置下会完全省略它，使这些提示字节保持不变。
	if hint := replyLanguageDirective(firstVerify(verify)); hint != "" {
		b.WriteString("\n")
		b.WriteString(hint)
		b.WriteString("\n")
	}
	return b.String()
}

// replyLanguageDirective 返回可选指令，追加到系统提示中要求模型用用户的语言回复；
// 没有显式且非默认的偏好配置时返回 ""。
//
// 唯一来源是 Config.Locale，该字段已由 --locale、ARGENT_LOCALE 和配置文件填充，
// 因此这里不新增任何标志或环境变量。英文（提示本身的语言）、"auto" 和空值表示
// "没有显式偏好"，不会产生指令，从而逐字节保留默认提示。原始标记会先经
// i18n.ParseOutputLanguagePreference 校验，再由 i18n.ReplyLanguageHint 渲染：
// 类似指令的、过长的或不受支持的值不会产生指令，而不会被回显进提示。
func replyLanguageDirective(cfg config.Config) string {
	raw := strings.TrimSpace(cfg.Locale)
	if raw == "" || strings.EqualFold(raw, "auto") {
		return ""
	}
	if code, ok := i18n.Canonical(raw); ok && code == i18n.EnUS {
		return ""
	}
	lang, ok := i18n.ParseOutputLanguagePreference(raw)
	if !ok || lang == "auto" {
		return ""
	}
	return i18n.ReplyLanguageHint(lang)
}

// buildDynamicContext 渲染必须留在缓存系统前缀之外的逐轮次值：工作目录、推理强度以及可选的输出 schema。
// 它刻意是用户角色的负载（不内置角色）：调用方把它作为独立消息注入，或前置到最后一条用户消息上，
// 因此当某个值改变时，只需重新发送这个短文本块。
// 其措辞与 buildSystemWithTrust 完全一致，因此移动它不会改变模型收到的内容。
func buildDynamicContext(root, thinking, schema string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Working directory: %s\n", root)
	if effort := strings.TrimSpace(thinking); effort != "" {
		fmt.Fprintf(&b, "Reasoning effort: %s. Spend accordingly.\n", effort)
	}
	if strings.TrimSpace(schema) != "" {
		b.WriteString("Reply with a single JSON document that conforms to this JSON Schema:\n")
		b.WriteString(strings.TrimSpace(schema))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// buildSystemWithTrust 是感知信任的提示构建器。trust 为 false 时，memory.LoadWithBoundary
// 不收集任何项目文档（只收集用户级文档）；为 true 时，它会从项目根目录向下到工作目录收集
// AGENTS.md/CLAUDE.md，并遵守 AGENTS.override.md。.argentignore 规则随后作为建议性的上下文大小过滤器应用，
// 绝不会作为安全控制。
//
// 它组合 buildStaticSystem 和 buildDynamicContext，使两个调用点对措辞保持单一事实来源。
// 希望获得稳定缓存前缀的调用方应直接使用这两个辅助函数，并把动态那一半作为用户消息注入，
// 而不是使用这个组合形式。
func buildSystemWithTrust(root string, env func(string) string, trust bool, thinking, schema string, verify ...config.Config) string {
	static := buildStaticSystem(root, env, trust, verify...)
	dynamic := buildDynamicContext(root, thinking, schema)
	var b strings.Builder
	b.WriteString(static)
	if dynamic != "" {
		b.WriteString("\n")
		b.WriteString(dynamic)
		b.WriteString("\n")
	}
	// 规则发现默认开启：只有显式给出假值词时这段为空，组合提示才回退到旧版输出。
	if block := ruleDiscoveryBlock(root, env, ""); block != "" {
		b.WriteString("\n")
		b.WriteString(block)
		b.WriteString("\n")
	}
	return b.String()
}

// ruleDiscoveryBlock 在 ARGENT_RULE_DISCOVERY 未显式关闭时（默认开启），扫描用户级（home）与项目级（root）
// 的规则文件，并渲染成一段"只列名、不加载全文"的系统提示文本。
//
// home/root 与 Resolved.Home/Resolved.Root 同源（homeDir(env) 与 buildSystem 的 root），
// 但这里刻意不经由 Resolved 传递，使提示构建器保持纯函数签名。
// mode 为空表示通用规则；当前提示路径没有明确模式，因此调用方传空串。
func ruleDiscoveryBlock(root string, env func(string) string, mode string) string {
	if !ruleDiscoveryEnabled(env) {
		return ""
	}
	return renderRuleDiscoveryBlock(memory.DiscoverRules(homeDir(env), root, mode))
}

// ruleDiscoveryEnabled 解释 ARGENT_RULE_DISCOVERY。与项目信任一致，这里默认开启：
// env 为 nil、未设置、空值或其它任何非假值都启用；只有假值词 0/false/no/off
// （忽略大小写与空白）才关闭，从而让默认提示即可列出发现的规则文件。
func ruleDiscoveryEnabled(env func(string) string) bool {
	if env == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(env("ARGENT_RULE_DISCOVERY"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// renderRuleDiscoveryBlock 把发现的规则元数据渲染为简短文本：列出 basename 与作用域，
// 并为 legacy 规则追加一次迁移提示。它不读取任何规则文件全文，保持提示词克制。
// 入参按 basename 再做一次防御性去重（DiscoverRules 本身已按 basename 去重）。
func renderRuleDiscoveryBlock(rules []memory.RuleFile) string {
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Discovered rule files (names only, contents not loaded):\n")
	seen := make(map[string]bool, len(rules))
	var legacyHint string
	for _, r := range rules {
		name := strings.TrimSpace(r.Basename)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		label := string(r.Scope)
		if r.Mode != "" {
			label += "/" + r.Mode
		}
		fmt.Fprintf(&b, "- %s (%s)\n", name, label)
		if hint := memory.RuleMigrationHint(r); hint != "" && legacyHint == "" {
			legacyHint = hint
		}
	}
	if legacyHint != "" {
		b.WriteString(legacyHint)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// firstVerify 取出通过可变参数提示构建器传入的可选验证配置。没有参数表示"没有配置检查"。
func firstVerify(verify []config.Config) config.Config {
	if len(verify) > 0 {
		return verify[0]
	}
	return config.Config{}
}

// platformFacts 渲染缓存稳定的平台上下文。它防止模型建议错误操作系统/shell 的命令，
// 或重复提议操作人员已经接好的检查。运行时和 shell 行始终存在；仓库行仅依赖工作区根目录的一次 stat；
// 验证指引只有在至少配置了一个命令时才会追加，因此默认（未配置）提示不会多出验证文本。
func platformFacts(root string, env func(string) string, verify config.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if shell := shellName(env); shell != "" {
		fmt.Fprintf(&b, "Shell: %s\n", shell)
	}
	if isGitRepo(root) {
		b.WriteString("Repository: this working directory is a git repository.\n")
	}
	if cmds := configuredVerifyCommands(verify); len(cmds) > 0 {
		b.WriteString("Verification: the workspace already runs these checks after edits: ")
		b.WriteString(strings.Join(cmds, "; "))
		b.WriteString("; do not suggest re-running them unless the user asks.\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// shellName 从环境推断交互式 shell。Windows 上 COMSPEC 优先；Unix 用 SHELL。
// 只暴露基本名称，filepath.Base 使该行保持简短，同时不会泄漏完整的 home/profile 路径。
func shellName(env func(string) string) string {
	if env == nil {
		return ""
	}
	for _, key := range []string{"COMSPEC", "SHELL"} {
		if value := strings.TrimSpace(env(key)); value != "" {
			return filepath.Base(value)
		}
	}
	return ""
}

// isGitRepo 报告工作区根目录本身是否持有 git 元数据。.git 目录和 .git 文件（工作树/子模块）都算。
// 只检查确切的根目录——向上遍历父目录会使缓存前缀依赖于临时目录碰巧所在的位置。
func isGitRepo(root string) bool {
	if root == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

// configuredVerifyCommands 以确定的顺序列出所有已配置的验证命令，
// 使缓存稳定的前缀不会在对按语言划分的 map 做 range 时发生漂移。
// VerifyCommands 的值按键排序；旧版 VerifyCommand 若设置则排在最前。
func configuredVerifyCommands(verify config.Config) []string {
	var out []string
	if len(verify.VerifyCommand) > 0 {
		out = append(out, strings.Join(verify.VerifyCommand, " "))
	}
	if len(verify.VerifyCommands) > 0 {
		keys := make([]string, 0, len(verify.VerifyCommands))
		for key := range verify.VerifyCommands {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if cmd := strings.TrimSpace(verify.VerifyCommands[key]); cmd != "" {
				out = append(out, cmd)
			}
		}
	}
	return out
}

// projectMemoryDocs 加载、排序并过滤要注入系统提示的指令文档。
// 它由提示构建器和 /context 报告共享，使两者描述相同的输入。
func projectMemoryDocs(root string, env func(string) string, trust bool) []memory.Doc {
	insts, err := memory.LoadWithBoundary(root, trust, memory.LoadOptions{Home: homeDir(env)})
	if err != nil {
		return nil
	}
	docs := memory.InstructionsToDocs(insts)
	// 嵌套仓库里父级 AGENTS.md 的规则常被子目录原样抄一遍。默认在注入前按
	// 规范化段落序列丢弃被更靠近 cwd 的文件完整包含的重复文档；仅当操作人员
	// 显式设 ARGENT_DEDUPE_CONTAINED_DOCS=0 时回退到旧的逐份注入行为。
	if defaultOnEnv(env, "ARGENT_DEDUPE_CONTAINED_DOCS") {
		docs = memory.DedupeContainedDocs(docs)
	}
	// LoadWithBoundary 按根优先返回，但 Block 的头部承诺条目从最具体（最近）的文件
	// 排列到最不具体的。这里反转顺序，使渲染出的文本块保持准确。
	for i, j := 0, len(docs)-1; i < j; i, j = i+1, j-1 {
		docs[i], docs[j] = docs[j], docs[i]
	}
	return filterIgnoredDocs(root, docs)
}

// defaultOnEnv 实现「新能力默认开启、环境变量仅作 =0 紧急回退」的统一口径：
// env 为 nil 或未设置该键时开启，只有精确等于 "0"（忽略两端空白）才关闭。
func defaultOnEnv(env func(string) string, key string) bool {
	if env == nil {
		return true
	}
	return strings.TrimSpace(env(key)) != "0"
}

// filterIgnoredDocs 丢弃路径被建议性 .argentignore 规则排除的文档。
// 这只是上下文大小方面的便利：漏判只会在提示中留下多余文本，不会造成安全漏洞。
// 发现过程是尽力而为的，因此不可读的忽略文件会保留原始文档。
func filterIgnoredDocs(root string, docs []memory.Doc) []memory.Doc {
	if len(docs) == 0 {
		return nil
	}
	rules, err := memory.LoadIgnore(root)
	if err != nil || rules == nil {
		return docs
	}
	out := docs[:0]
	for _, doc := range docs {
		rel, err := filepath.Rel(root, doc.Path)
		if err == nil && rules.Ignored(rel) {
			continue
		}
		out = append(out, doc)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// 工作区规则文件在会话内变更时的重注入。
//
// 用户在长会话中途编辑 AGENTS.md 纠正规则时，agent 不应等到重启才生效。
// InstructionReconciler 在轮次边界对当前加载的指令文件算 SHA-256，与上次注入
// 的基线比对；变化时产出有界的 `Updated instructions from: <path>` / 移除通知，
// 复用既有 steering 通道在下次请求前作为用户轮次注入。无变化零开销。

const (
	// instructionTextBudget 是单条重注入消息中携带的新正文上限（字节）。
	instructionTextBudget = 4096
	// instructionMsgBudget 是单条消息的总上限（字节），含头部与注入警示。
	instructionMsgBudget = 8192
)

// contextSink 是重注入消息的落点。agent.HookContextQueue 的 Add 满足它，
// 因此 app 侧接线只需把队列交给 ReconcileInto，无需引入具体类型依赖。
type contextSink interface {
	Add(string) bool
}

// InstructionReconciler 持有跨轮次的指令摘要基线。必须在一次会话中创建一次，
// 并在每次轮次边界调用 Reconcile/ReconcileInto。零值可用，但建议用构造函数。
type InstructionReconciler struct {
	mu       sync.Mutex
	baseline memory.InstructionState
	seeded   bool
}

// NewInstructionReconciler 返回一个空的基线协调器。
func NewInstructionReconciler() *InstructionReconciler { return &InstructionReconciler{} }

// Reset 丢弃基线，使下一次 Reconcile 只建立基线而不产出消息。会话切换时调用。
func (r *InstructionReconciler) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.baseline = memory.InstructionState{}
	r.seeded = false
}

// Reconcile 比较当前指令文件与基线并返回有界的重注入消息。
// 首次调用只建立基线、不产出消息（会话启动无需报告「已加载」）；
// 后续仅在摘要变化时产出。ARGENT_INSTRUCTION_RECONCILE=0 时整段逻辑关闭。
func (r *InstructionReconciler) Reconcile(root string, env func(string) string, trust bool) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !defaultOnEnv(env, "ARGENT_INSTRUCTION_RECONCILE") {
		return nil
	}
	current := memory.SnapshotInstructions(projectMemoryDocs(root, env, trust))
	if !r.seeded {
		r.baseline, r.seeded = current, true
		return nil
	}
	if current.Version() == r.baseline.Version() {
		return nil
	}
	changes := memory.DiffInstructionStates(r.baseline, current)
	r.baseline = current
	return renderInstructionChanges(changes)
}

// ReconcileInto 是 Reconcile 的接线便利形式：把产出的消息逐条交给 sink
// （通常是 agent.HookContextQueue，从而在下一轮由 agent 经 steering 注入），
// 返回实际入队的条数。
func (r *InstructionReconciler) ReconcileInto(sink contextSink, root string, env func(string) string, trust bool) int {
	if sink == nil {
		return 0
	}
	messages := r.Reconcile(root, env, trust)
	added := 0
	for _, msg := range messages {
		if sink.Add(msg) {
			added++
		}
	}
	return added
}

// renderInstructionChanges 把结构化的指令差异渲染为有界、经脱敏与注入扫描的
// 用户轮次文本。高风险注入只前置一行警示、绝不改写或过滤原文任何字节。
func renderInstructionChanges(changes []memory.InstructionChange) []string {
	var out []string
	for _, change := range changes {
		var b strings.Builder
		switch change.Kind {
		case memory.InstructionRemoved:
			fmt.Fprintf(&b, "Instructions removed: %s", change.Path)
		case memory.InstructionAdded, memory.InstructionUpdated:
			fmt.Fprintf(&b, "Updated instructions from: %s\n%s", change.Path, clipInstructionText(change.Text, instructionTextBudget))
		default:
			continue
		}
		msg := b.String()
		msg = security.Redact(msg)
		for _, finding := range security.ScanPromptInjection(msg) {
			if finding.Severity == security.InjectionSeverityHigh {
				msg = "[注意：内容含疑似指令注入]\n" + msg
				break
			}
		}
		out = append(out, clipInstructionText(msg, instructionMsgBudget))
	}
	return out
}

// clipInstructionText 按 rune 安全裁剪到至多 limit 字节，避免在 CJK/emoji 中间切断。
func clipInstructionText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	k := limit
	for k > 0 && !utf8.RuneStart(text[k]) {
		k--
	}
	return text[:k]
}

// loadOutputSchema 接受内联的 schema 文本或文件路径。
func loadOutputSchema(root, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	text := value
	if !strings.HasPrefix(value, "{") {
		path := value
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("--output-schema: %w", err)
		}
		text = string(data)
	}
	if !json.Valid([]byte(text)) {
		return "", fmt.Errorf("--output-schema: the schema is not valid JSON")
	}
	return text, nil
}
