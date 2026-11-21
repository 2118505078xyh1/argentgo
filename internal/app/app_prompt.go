package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/i18n"
	"github.com/2118505078xyh1/argentgo/internal/memory"
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
	if dynamic == "" {
		return static
	}
	return static + "\n" + dynamic + "\n"
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
	// LoadWithBoundary 按根优先返回，但 Block 的头部承诺条目从最具体（最近）的文件
	// 排列到最不具体的。这里反转顺序，使渲染出的文本块保持准确。
	for i, j := 0, len(docs)-1; i < j; i, j = i+1, j-1 {
		docs[i], docs[j] = docs[j], docs[i]
	}
	return filterIgnoredDocs(root, docs)
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
