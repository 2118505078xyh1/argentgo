// Package security 是每次工具调用前的关卡：声明式策略、审批提示，以及仅追加的审计记录。
//
// 两层保持独立，与 Rust 参考实现一致：
//
//   - Policy 负责判定。命中的规则会拦截；空的规则集除了词法上逃逸出
//     工作区根目录的路径外，不拦截任何东西。
//   - Audit 负责记录。每个决定都会写入 JSONL 文件，这样"这个 agent 对我的
//     机器做了什么"就有了一个独立于会话记录的答案。
//
// 本包是自包含的：只用标准库，不依赖其他 internal 包。
package security

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Decision 是一次工具调用的判定结果。
type Decision int

const (
	// Allow 直接运行调用，不询问任何人。
	Allow Decision = iota
	// Ask 交由审批器决定。
	Ask
	// Deny 无论审批器怎么说都会停止调用。
	Deny
)

// String 返回用于日志的小写名称。
func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	default:
		return "deny"
	}
}

// ParseDecision 是 Decision.String 的逆操作。
func ParseDecision(text string) (Decision, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "allow", "allowed":
		return Allow, true
	case "ask":
		return Ask, true
	case "deny", "denied":
		return Deny, true
	}
	return Ask, false
}

// Rule 是有序 allow/ask/deny 表中的一行。空字段是通配符。当多行匹配同一次
// 调用时，取最严格的判定（deny > ask > allow），因此较早的 allow 永远不会
// 缓和后面的 deny 或 ask。行序只影响同等严格程度下报告哪个原因。
type Rule struct {
	Decision Decision
	Tool     string
	Path     string
	Command  string
}

// PolicyConfig 是声明式规则集。零值表示"无规则"。
type PolicyConfig struct {
	// DenyPaths 是任何工具都不得触碰的路径模式。
	DenyPaths []string
	// AllowPaths 非空时，会关闭所有未列出的路径。
	AllowPaths []string
	// DenyCommands 是用于拦截命令的子串或 glob 模式。
	DenyCommands []string
	// DenyCommandPaths 拦截提及被拒绝路径片段的命令。
	DenyCommandPaths []string
	// DenyTools 是永不被允许的工具名。
	DenyTools []string
	// AllowTools 非空时，是唯一被允许的工具名集合。
	AllowTools []string
	// Rules 是 allow/ask/deny 表。会考虑每一条匹配的行，
	// 取最严格的判定（deny > ask > allow）。
	Rules []Rule
	// ReadOnlyTools 扩展内置的安全工具列表。
	ReadOnlyTools []string
	// MutatingTools 扩展内置的修改类工具列表。
	MutatingTools []string
	// WriteRoots 扩展修改类路径工具可写入的根目录。即使该列表为空，
	// 工作区根目录也始终包含在内。
	WriteRoots []string
}

// Result 携带判定结果及其背后人类可读的原因。
type Result struct {
	Decision Decision
	Reason   string
}

var defaultReadOnlyTools = map[string]bool{
	"read": true, "ls": true, "grep": true, "find": true, "glob": true,
	"todo": true, "web_fetch": true, "git_status": true,
}

var defaultMutatingTools = map[string]bool{
	"write": true, "edit": true, "multi_edit": true, "bash": true,
	"undo": true, "restore": true,
	// apply_patch 会改写工作区文件，必须纳入修改类工具，让其文件路径
	// 同样受 writeRoots 边界约束（与 write/edit/multi_edit 同级）。
	"apply_patch": true,
}

// Policy 是在审批之前评估的硬关卡。
type Policy struct {
	root             string
	denyPaths        []string
	allowPaths       []string
	denyCommands     []string
	denyCommandPaths []string
	denyTools        map[string]bool
	allowTools       map[string]bool
	rules            []Rule
	// toolKinds 把工具名映射到它是否会修改工作区。
	toolKinds map[string]bool
	// writeRoots 限定修改类路径工具。只读工具不受检查。
	writeRoots []string
}

// NewPolicy 构建一个以 root（通常是工作区 cwd）为根的策略。
// 工具参数中的相对路径都相对于它解析。
func NewPolicy(root string, cfg PolicyConfig) *Policy {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	p := &Policy{
		root:             filepath.Clean(root),
		denyPaths:        cfg.DenyPaths,
		allowPaths:       cfg.AllowPaths,
		denyCommands:     cfg.DenyCommands,
		denyCommandPaths: cfg.DenyCommandPaths,
		denyTools:        make(map[string]bool, len(cfg.DenyTools)),
		allowTools:       make(map[string]bool, len(cfg.AllowTools)),
		rules:            cfg.Rules,
		toolKinds:        make(map[string]bool, len(defaultMutatingTools)+len(cfg.MutatingTools)),
		writeRoots:       WriteRootsFrom(root, cfg.WriteRoots),
	}
	for name := range defaultReadOnlyTools {
		p.toolKinds[name] = false
	}
	for name, mutating := range defaultMutatingTools {
		p.toolKinds[name] = mutating
	}
	for _, name := range cfg.ReadOnlyTools {
		p.toolKinds[name] = false
	}
	for _, name := range cfg.MutatingTools {
		p.toolKinds[name] = true
	}
	for _, name := range cfg.DenyTools {
		p.denyTools[name] = true
	}
	for _, name := range cfg.AllowTools {
		p.allowTools[name] = true
	}
	return p
}

// DefaultPolicy 是一个以 root 为根的空规则集：只拦截工作区逃逸。
func DefaultPolicy(root string) *Policy {
	return NewPolicy(root, PolicyConfig{})
}

// PolicyFromJSON 解析设置文件中的 `permissions` 块：
//
//	{"denyPaths":["**/.env"],"denyCommands":["git push --force"],
//	 "denyTools":["bash"],"allowTools":["read","ls"],
//	 "rules":[{"decision":"ask","tool":"bash","command":"git push*"}]}
//
// 列表字段也接受以逗号分隔的字符串。
func PolicyFromJSON(root string, data []byte) (*Policy, error) {
	policy, _, err := ParsePolicyWithIssues(root, data)
	return policy, err
}

// Root 是用于解析相对路径的工作区根目录。
func (p *Policy) Root() string { return p.root }

// IsEmpty 报告是否配置了任何规则。
func (p *Policy) IsEmpty() bool {
	return len(p.denyPaths) == 0 && len(p.allowPaths) == 0 &&
		len(p.denyCommands) == 0 && len(p.denyCommandPaths) == 0 &&
		len(p.denyTools) == 0 && len(p.allowTools) == 0 && len(p.rules) == 0
}

// Check 评估一次工具调用。它满足测试框架的 Guard 契约；当还需要原因时
// 请使用 Evaluate。
func (p *Policy) Check(name string, args json.RawMessage) Decision {
	return p.Evaluate(name, args).Decision
}

// Evaluate 返回判定结果及其背后原因。单纯的 Allow 附带空原因。
func (p *Policy) Evaluate(name string, args json.RawMessage) Result {
	name = strings.TrimSpace(name)
	if name == "" {
		return Result{Deny, "the tool name is empty"}
	}
	if p.denyTools[name] {
		return Result{Deny, fmt.Sprintf("the tool %q is denied by policy", name)}
	}
	if len(p.allowTools) > 0 && !p.allowTools[name] {
		return Result{Deny, fmt.Sprintf("the tool %q is not on the allow list", name)}
	}
	fields, err := parseArgs(args)
	if err != nil {
		return Result{Ask, fmt.Sprintf("the %s arguments are not a JSON object: %v", name, err)}
	}
	for _, path := range pathValues(fields) {
		if res := p.checkPath(path); res.Decision != Allow {
			return res
		}
	}
	if command, ok := fields["command"].(string); ok {
		if res := p.checkCommand(command); res.Decision != Allow {
			return res
		}
	}
	// 写入被限定在工作区加上任何 --add-dir 根目录之内。只读工具刻意跳过
	// 此检查：路径可以可读但不可写，而且上面的路径规则仍然适用于每个工具。
	if mutating, known := p.toolKinds[name]; known && mutating {
		if res := p.checkWriteRoots(fields); res.Decision != Allow {
			return res
		}
	}
	if res, ok := p.ruleDecision(name, fields); ok {
		return res
	}
	if mutating, known := p.toolKinds[name]; known {
		if mutating {
			return Result{Ask, fmt.Sprintf("the mutating tool %q needs operator approval", name)}
		}
		return Result{Allow, ""}
	}
	return Result{Ask, fmt.Sprintf("the unknown tool %q needs operator approval", name)}
}

// pathArgKeys 列出携带文件系统路径的参数名。大多数内置工具使用 "path"，
// 但 diff 工具写作 "path_a"/"path_b"，Claude 风格的工具使用
// "file_path"/"filePath"。遗漏其中任何一个名字过去都会绕过所有路径规则：
// checkPath 在调用中看不到路径，就让它落到 Ask（以及自动批准）。请让此列表
// 与内置工具 schema 和常见的 MCP 参数约定保持同步。
var pathArgKeys = []string{
	"path", "file", "dir", "directory", "target",
	"path_a", "path_b",
	"file_path", "filePath", "filepath",
	"old_path", "new_path", "notebook_path",
}

// pathArrayKeys 列出其字符串元素是路径的数组参数。checkpoint 恢复清单
// 使用 "paths"；context_pack 使用 "files"。
var pathArrayKeys = []string{"paths", "files"}

// checkPath 对一个参数应用 deny/allow 路径规则，并对每个工具（包括只读工具）
// 强制工作区边界。词法上爬出工作区根目录的路径总会被拒绝；看起来在内部、
// 但通过符号链接或 Windows junction 解析到外部的路径同样被拒绝。
func (p *Policy) checkPath(path string) Result {
	resolved, escape := p.resolve(path)
	if escape {
		return Result{Deny, fmt.Sprintf("the path %q escapes the workspace root %q", path, p.root)}
	}
	real, err := ResolveRealPath(resolved)
	if err != nil {
		return Result{Deny, fmt.Sprintf(
			"the path %q could not be canonicalized for policy checking (%v); denying to avoid an unresolved symlink target",
			path, err)}
	}
	// 隐藏逃逸检查。即使没有配置 deny/allow 路径也会运行：仅仅因为策略没有
	// 显式路径规则，对 <root>/link/id_rsa 的读取就不能变成对外部文件的读取。
	if within(resolved, p.writeRoots) && !within(real, p.realRoots()) {
		return Result{Deny, fmt.Sprintf(
			"the path %q resolves outside the workspace root %q through a symlink or junction", path, p.root)}
	}
	lexical := normalize(resolved)
	canonical := normalize(real)
	for _, pattern := range p.denyPaths {
		if GlobMatch(pattern, lexical) || GlobMatch(pattern, canonical) {
			return Result{Deny, fmt.Sprintf("the path %q is denied by policy (rule %q)", path, pattern)}
		}
	}
	if len(p.allowPaths) > 0 {
		if !anyMatch(p.allowPaths, lexical) || !anyMatch(p.allowPaths, canonical) {
			return Result{Deny, fmt.Sprintf("the path %q is outside the allowed set", path)}
		}
	}
	return Result{Allow, ""}
}

// realRoots 解析每个配置的根目录，使通过链接到达的根目录仍算作内部。
// 无法解析的目录回退到其词法写法；显式根目录是操作员配置，不是工具输入。
func (p *Policy) realRoots() []string {
	out := make([]string, 0, len(p.writeRoots))
	for _, root := range p.writeRoots {
		if real, err := ResolveRealPath(root); err == nil {
			out = append(out, real)
		} else {
			out = append(out, filepath.Clean(root))
		}
	}
	return out
}

// checkWriteRoots 对修改类工具每个路径参数应用写入边界。相对的 `..` 逃逸
// 已被 checkPath 拒绝；这里 InWriteRoots 捕获落在允许根目录之外的绝对路径，
// 根目录内的受保护子路径保持只读。
func (p *Policy) checkWriteRoots(fields map[string]any) Result {
	for _, value := range pathValues(fields) {
		if err := InWriteRoots(value, p.writeRoots); err != nil {
			return Result{Deny, fmt.Sprintf("the path %q is not writable: %v", value, err)}
		}
		if protected, reason := protectedInWriteRoots(value, p.writeRoots); protected {
			return Result{Deny, fmt.Sprintf("the path %q is not writable: %s", value, reason)}
		}
	}
	return Result{Allow, ""}
}

// pathValues 收集每个路径参数，包括 checkpoint 恢复动作和 context_pack
// 使用的数组形式。顺序是稳定的：先按 pathArgKeys 顺序列出标量键，再按
// pathArrayKeys 顺序列出每个已知数组，其元素按调用方提供的顺序排列。
func pathValues(fields map[string]any) []string {
	var paths []string
	for _, key := range pathArgKeys {
		if value, ok := fields[key].(string); ok {
			paths = append(paths, value)
		}
	}
	for _, key := range pathArrayKeys {
		if list, ok := fields[key].([]any); ok {
			for _, item := range list {
				if value, ok := item.(string); ok {
					paths = append(paths, value)
				}
			}
		}
	}
	return paths
}

// checkCommand 应用命令规则。匹配不区分大小写。
func (p *Policy) checkCommand(command string) Result {
	lowered := strings.ToLower(command)
	for _, pattern := range p.denyCommands {
		if strings.Contains(lowered, strings.ToLower(pattern)) || GlobMatch(pattern, lowered) {
			return Result{Deny, fmt.Sprintf("the command is denied by policy (rule %q)", pattern)}
		}
	}
	for _, pattern := range p.denyCommandPaths {
		needle := strings.ToLower(pattern)
		if strings.Contains(lowered, needle) {
			return Result{Deny, fmt.Sprintf("the command touches a denied path (rule %q)", pattern)}
		}
		if expanded := strings.ToLower(expandHome(pattern)); expanded != needle && strings.Contains(lowered, expanded) {
			return Result{Deny, fmt.Sprintf("the command touches a denied path (rule %q)", pattern)}
		}
	}
	return Result{Allow, ""}
}

// ruleDecision 遍历表，返回所有匹配行中最严格的判定：deny > ask > allow。
// allow 行永远不会降低后面某行已经产生的 ask 或 deny，因此重排规则不会
// 悄悄扩大访问权限。命中的 deny 是绝对的，会立即停止遍历（它已经是
// 最严格的结果）。没有行匹配时，采用调用方的默认行为。
func (p *Policy) ruleDecision(name string, fields map[string]any) (Result, bool) {
	var best Decision
	matched := false
	// 一次性收集每个路径资源。pathValues 包含 checkpoint 恢复和 context_pack
	// 使用的路径数组；这里只扫描标量键过去会隐藏针对数组条目的路径规则。在
	// 多资源调用中，这会让标量路径上的 Ask 掩盖数组条目上的 Deny，因为 deny
	// 规则从未命中，从而掉出了取最严格判定的遍历。
	paths := pathValues(fields)
	for _, rule := range p.rules {
		if rule.Tool != "" && rule.Tool != "*" && rule.Tool != name {
			continue
		}
		if rule.Path != "" {
			matchedPath := false
			for _, value := range paths {
				resolved, escape := p.resolve(value)
				if escape {
					matchedPath = true
					break
				}
				lexical := normalize(resolved)
				if GlobMatch(rule.Path, lexical) {
					matchedPath = true
					break
				}
				if canonical, err := ResolveRealPath(resolved); err == nil && GlobMatch(rule.Path, normalize(canonical)) {
					matchedPath = true
					break
				}
			}
			if !matchedPath {
				continue
			}
		}
		if rule.Command != "" {
			command, ok := fields["command"].(string)
			if !ok || !commandMatches(rule.Command, command) {
				continue
			}
		}
		// Deny 是最严格的判定，也是硬性停止：后面的行无法覆盖它，
		// 因此没有理由继续扫描。
		if rule.Decision == Deny {
			return Result{Deny, fmt.Sprintf("the %s call is denied by the permissions table", name)}, true
		}
		if !matched || rule.Decision > best {
			best = rule.Decision
		}
		matched = true
	}
	if !matched {
		return Result{}, false
	}
	reason := ""
	if best == Ask {
		reason = fmt.Sprintf("the %s call requires approval per the permissions table", name)
	}
	return Result{best, reason}, true
}

// resolve 把工具的路径参数转成绝对路径。返回的 bool 报告是否词法上
// 逃逸出工作区根目录（相对路径向外爬升）。
func (p *Policy) resolve(path string) (string, bool) {
	candidate := expandHome(strings.TrimSpace(path))
	if candidate == "" {
		candidate = "."
	}
	relative := !filepath.IsAbs(candidate)
	if relative {
		candidate = filepath.Join(p.root, candidate)
	}
	candidate = filepath.Clean(candidate)
	if relative {
		if rel, err := filepath.Rel(p.root, candidate); err != nil || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return candidate, true
		}
	}
	return candidate, false
}

func isMissing(err error) bool {
	return os.IsNotExist(err) || strings.Contains(strings.ToLower(err.Error()), "not a directory")
}

func anyMatch(patterns []string, path string) bool {
	for _, pattern := range patterns {
		if GlobMatch(pattern, path) {
			return true
		}
	}
	return false
}

func parseArgs(args json.RawMessage) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(args))
	if trimmed == "" || trimmed == "null" {
		return map[string]any{}, nil
	}
	var fields map[string]any
	if err := json.Unmarshal(args, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = map[string]any{}
	}
	normalizeCommandField(fields)
	return fields, nil
}

// normalizeCommandField 镜像工具层的别名处理。诸如 powershell 的工具接受
// `script` 作为 `command` 的别名，并在 `command` 为空（含空白）时执行它。
// checkCommand 和 ruleDecision 只读取 `command`，因此没有此规范化时，只要
// 重命名字段就能把硬 deny 降级为 Ask —— 并在 --yolo 下被自动执行。
//
// 该回退刻意保守：显式非空的 `command` 始终优先，绝不会被覆盖。
// 只改动策略评估器使用的 map；转发给工具的原始参数保持不变。
func normalizeCommandField(fields map[string]any) {
	if command, ok := fields["command"].(string); ok && strings.TrimSpace(command) != "" {
		return
	}
	script, ok := fields["script"].(string)
	if !ok || strings.TrimSpace(script) == "" {
		return
	}
	fields["command"] = script
}

func stringList(raw json.RawMessage) ([]string, error) {
	var items []string
	if err := json.Unmarshal(raw, &items); err == nil {
		return items, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, fmt.Errorf("expected a list of strings or a comma-separated string")
	}
	out := make([]string, 0, 4)
	for _, item := range strings.Split(text, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out, nil
}

// parseRules 是不带诊断信息的版本，供只需要可用规则的调用方使用。
// 被跳过规则的细节在 parseRulesWithIssues 中。
func parseRules(raw json.RawMessage) ([]Rule, error) {
	rules, _, err := parseRulesWithIssues(raw)
	return rules, err
}

// MutatingTool 报告内置表是否认为某工具是修改类的。
func MutatingTool(name string) bool {
	return defaultMutatingTools[name]
}

func commandMatches(pattern, command string) bool {
	lowered := strings.ToLower(command)
	needle := strings.ToLower(pattern)
	if strings.Contains(lowered, needle) {
		return true
	}
	return matchChars([]rune(needle), []rune(lowered))
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
